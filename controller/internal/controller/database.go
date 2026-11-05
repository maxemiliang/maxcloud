package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// DatabaseFinalizer holds a Database until its backup bucket is dealt with.
const DatabaseFinalizer = "maxcloud.gronblom.dev/database"

// DefaultPostgresImage is the Postgres the databases run, unless the
// controller is told otherwise (MAXCLOUD_POSTGRES_IMAGE).
// renovate: datasource=docker depName=ghcr.io/cloudnative-pg/postgresql
const DefaultPostgresImage = "ghcr.io/cloudnative-pg/postgresql:18.6-minimal-trixie"

// Condition reason.
const ReasonBlocked = "Blocked"

// DatabaseReconciler gives an app a Postgres database (a CloudNativePG
// Cluster) that backs itself up to the app's bucket mc-<app>--db and, when
// its Cluster is gone, comes back from those backups by itself. See
// decide for how it chooses between a new database and a restore.
type DatabaseReconciler struct {
	client.Client
	Identity *Identity
	Storage  objectstorage.Storage
	// Objects reads and writes the bucket as the controller's own S3 user
	// (the marker, which the app can't write).
	Objects  objectstorage.Objects
	Endpoint string
	Image    string
	// StorageClass of new Clusters' disks, "" for the cluster's default.
	// Existing Clusters keep theirs.
	StorageClass string
	// Now is time.Now, replaceable in tests.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=databases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=databases/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters;scheduledbackups;backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=barmancloud.cnpg.io,resources=objectstores,verbs=get;list;watch;create;update;patch;delete

func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var db v1.Database
	if err := r.Get(ctx, req.NamespacedName, &db); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	app := db.Namespace
	setDefaults(&db)

	ns, err := r.namespace(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	tenant := ns != nil && ns.Labels[TenantLabel] == "true"
	if !db.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &db, tenant)
	}
	if !tenant {
		return ctrl.Result{}, r.setStatus(ctx, &db, "", metav1.ConditionFalse, ReasonNotTenant,
			fmt.Sprintf("namespace %s isn't an app on the run platform (no %s label)", app, TenantLabel))
	}
	// A namespace being deleted takes the Cluster with it; that isn't a
	// Cluster to bring back.
	if ns.Status.Phase == corev1.NamespaceTerminating {
		return ctrl.Result{}, nil
	}

	bucket, err := naming.Bucket(app, naming.DatabaseBucket)
	if err != nil {
		return ctrl.Result{}, r.setStatus(ctx, &db, "", metav1.ConditionFalse, ReasonBadName, err.Error())
	}
	db.Status.Bucket = bucket

	if controllerutil.AddFinalizer(&db, DatabaseFinalizer) {
		if err := r.Update(ctx, &db); err != nil {
			return ctrl.Result{}, err
		}
		setDefaults(&db)
	}

	res, err := r.reconcile(ctx, &db, app, bucket)
	if err != nil {
		if serr := r.setStatus(ctx, &db, "", metav1.ConditionFalse, ReasonAPIError, err.Error()); serr != nil {
			return ctrl.Result{}, errors.Join(err, serr)
		}
		return ctrl.Result{}, err
	}
	return res, nil
}

func (r *DatabaseReconciler) reconcile(ctx context.Context, db *v1.Database, app, bucket string) (ctrl.Result, error) {
	if err := r.Identity.Ensure(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(clusterGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: app, Name: db.Name}, c)
	if apierrors.IsNotFound(err) {
		return r.create(ctx, db, bucket)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(c, db) {
		return r.block(ctx, db, fmt.Sprintf(
			"a Cluster %s already exists and isn't this Database's (a database.yaml in the app's repo?): "+
				"remove it, or the registry's db field", db.Name))
	}
	return r.update(ctx, db, bucket, c)
}

// create brings a missing Cluster back: a new database, or a restore.
func (r *DatabaseReconciler) create(ctx context.Context, db *v1.Database, bucket string) (ctrl.Result, error) {
	recoverFrom, err := recoverFromAnnotation(db)
	if err != nil {
		return r.block(ctx, db, err.Error())
	}
	view, err := r.look(ctx, bucket, recoverFrom)
	if err != nil {
		var bad badMarker
		if errors.As(err, &bad) {
			return r.block(ctx, db, err.Error())
		}
		return ctrl.Result{}, err
	}
	p := decide(view, recoverFrom)
	if p.Action == actBlock {
		return r.block(ctx, db, p.Reason)
	}

	if err := r.Storage.EnsureBucket(ctx, bucket); err != nil {
		return ctrl.Result{}, err
	}
	// The marker first: if this goes no further, the next try knows what
	// was being done.
	m := Marker{Generation: p.Generation, State: StateInitializing, Updated: r.now()}
	phase := v1.PhaseCreating
	if p.Action == actRestore {
		m.State, m.RestoredFrom = StateRestoring, p.From
		phase = v1.PhaseRestoring
	}
	if err := r.Objects.PutObject(ctx, bucket, markerKey, m.json()); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info(p.Reason, "generation", p.Generation, "from", p.From)

	if err := r.apply(ctx, db, objectStore(db, bucket, r.Endpoint)); err != nil {
		return ctrl.Result{}, err
	}
	c := cluster(db, r.Image, r.StorageClass, p.Generation, p.From)
	if err := controllerutil.SetControllerReference(db, c, r.Scheme()); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, c); err != nil {
		return ctrl.Result{}, err
	}
	if recoverFrom > 0 {
		patch := client.MergeFrom(db.DeepCopy())
		delete(db.Annotations, v1.RecoverFromAnnotation)
		if err := r.Patch(ctx, db, patch); err != nil {
			return ctrl.Result{}, err
		}
		setDefaults(db)
	}

	db.Status.Generation, db.Status.RestoredFrom = p.Generation, p.From
	meta.RemoveStatusCondition(&db.Status.Conditions, "Blocked")
	return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, db, phase, metav1.ConditionFalse, ReasonInProgress, p.Reason)
}

// update keeps an existing Cluster in line with the spec, and marks its
// generation ready once it has its own base backup.
func (r *DatabaseReconciler) update(ctx context.Context, db *v1.Database, bucket string, c *unstructured.Unstructured) (ctrl.Result, error) {
	gen, err := parseServerName(clusterServerName(c))
	if err != nil {
		return r.block(ctx, db, fmt.Sprintf("Cluster %s: %v", db.Name, err))
	}
	if err := r.apply(ctx, db, objectStore(db, bucket, r.Endpoint)); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.patchCluster(ctx, db, c); err != nil {
		return ctrl.Result{}, err
	}
	db.Status.Generation = gen
	meta.RemoveStatusCondition(&db.Status.Conditions, "Blocked")

	if !clusterReady(c) {
		phase := db.Status.Phase
		if phase == "" || phase == v1.PhaseReady || phase == v1.PhaseBlocked {
			phase = v1.PhaseCreating
		}
		return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, db, phase, metav1.ConditionFalse, ReasonInProgress,
			"waiting for Cluster "+db.Name+" to be ready")
	}
	if err := r.apply(ctx, db, scheduledBackup(db)); err != nil {
		return ctrl.Result{}, err
	}

	data, found, err := r.Objects.GetObject(ctx, bucket, markerKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	var m *Marker
	if found {
		if m, err = parseMarker(data); err != nil {
			return r.block(ctx, db, fmt.Sprintf("%s in %s: %v", markerKey, bucket, err))
		}
	}
	if m == nil || m.Generation != gen || m.State != StateReady {
		// This generation has no base backup of its own yet: take one,
		// and mark it ready when it's there.
		base, err := r.hasBase(ctx, bucket, gen)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !base {
			if err := r.ensureFirstBackup(ctx, db, gen); err != nil {
				return ctrl.Result{}, err
			}
			phase := db.Status.Phase
			if phase == "" || phase == v1.PhaseReady || phase == v1.PhaseBlocked {
				phase = v1.PhaseCreating
			}
			return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, db, phase, metav1.ConditionFalse, ReasonInProgress,
				fmt.Sprintf("Cluster %s is up; waiting for generation %d's first base backup", db.Name, gen))
		}
		ready := Marker{Generation: gen, State: StateReady, Updated: r.now()}
		if m != nil && m.Generation == gen {
			ready.RestoredFrom = m.RestoredFrom
		}
		if err := r.Objects.PutObject(ctx, bucket, markerKey, ready.json()); err != nil {
			return ctrl.Result{}, err
		}
		log.FromContext(ctx).Info("generation has its base backup", "generation", gen)
	}
	return ctrl.Result{RequeueAfter: resync}, r.setStatus(ctx, db, v1.PhaseReady, metav1.ConditionTrue, ReasonReady,
		fmt.Sprintf("Cluster %s is ready, backing up to %s/%s", db.Name, bucket, serverName(gen)))
}

// patchCluster changes what can change on a live Cluster: instances, PDB,
// resources and a bigger disk. Never the bootstrap or where it archives.
func (r *DatabaseReconciler) patchCluster(ctx context.Context, db *v1.Database, c *unstructured.Unstructured) error {
	want := cluster(db, r.Image, r.StorageClass, 0, 0)
	before := c.DeepCopy()
	for _, f := range [][]string{{"spec", "instances"}, {"spec", "enablePDB"}, {"spec", "resources"}} {
		v, _, _ := unstructured.NestedFieldCopy(want.Object, f...)
		if err := unstructured.SetNestedField(c.Object, v, f...); err != nil {
			return err
		}
	}
	cur, _, _ := unstructured.NestedString(c.Object, "spec", "storage", "size")
	if q, err := resource.ParseQuantity(cur); err != nil || db.Spec.Size.Cmp(q) > 0 {
		if err := unstructured.SetNestedField(c.Object, db.Spec.Size.String(), "spec", "storage", "size"); err != nil {
			return err
		}
	}
	return r.Patch(ctx, c, client.MergeFrom(before))
}

// look reads what the bucket holds that decide needs.
func (r *DatabaseReconciler) look(ctx context.Context, bucket string, recoverFrom int64) (bucketView, error) {
	v := bucketView{Base: map[int64]bool{}, Any: map[int64]bool{}}
	keys, err := r.Objects.Keys(ctx, bucket, "", 2)
	if errors.Is(err, objectstorage.ErrNoSuchBucket) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Exists = true
	v.Empty = len(keys) == 0 || (len(keys) == 1 && keys[0] == markerKey)

	data, found, err := r.Objects.GetObject(ctx, bucket, markerKey)
	if err != nil {
		return v, err
	}
	if found {
		m, err := parseMarker(data)
		if err != nil {
			return v, badMarker{fmt.Errorf("%s in %s: %w", markerKey, bucket, err)}
		}
		v.Marker = m
	}
	for _, gen := range generationsToCheck(v.Marker, recoverFrom) {
		objs, err := r.Objects.Keys(ctx, bucket, serverName(gen)+"/", 1)
		if err != nil {
			return v, err
		}
		v.Any[gen] = len(objs) > 0
		if v.Base[gen], err = r.hasBase(ctx, bucket, gen); err != nil {
			return v, err
		}
	}
	return v, nil
}

// hasBase reports whether generation gen has a finished base backup: a
// barman backup.info that says status=DONE.
func (r *DatabaseReconciler) hasBase(ctx context.Context, bucket string, gen int64) (bool, error) {
	keys, err := r.Objects.Keys(ctx, bucket, serverName(gen)+"/base/", 1000)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if !strings.HasSuffix(k, "/backup.info") {
			continue
		}
		data, found, err := r.Objects.GetObject(ctx, bucket, k)
		if err != nil {
			return false, err
		}
		if found && strings.Contains(string(data), "status=DONE") {
			return true, nil
		}
	}
	return false, nil
}

// apply creates or updates an object the Database owns, as built.
func (r *DatabaseReconciler) apply(ctx context.Context, db *v1.Database, want *unstructured.Unstructured) error {
	if err := controllerutil.SetControllerReference(db, want, r.Scheme()); err != nil {
		return err
	}
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(want.GroupVersionKind())
	err := r.Get(ctx, client.ObjectKeyFromObject(want), cur)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, want)
	}
	if err != nil {
		return err
	}
	before := cur.DeepCopy()
	cur.Object["spec"] = want.Object["spec"]
	cur.SetLabels(want.GetLabels())
	cur.SetOwnerReferences(want.GetOwnerReferences())
	return r.Patch(ctx, cur, client.MergeFrom(before))
}

// ensureFirstBackup makes sure generation gen's first base backup is
// running or done; one that failed is tried again.
func (r *DatabaseReconciler) ensureFirstBackup(ctx context.Context, db *v1.Database, gen int64) error {
	want := firstBackup(db, gen)
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(backupGVK)
	err := r.Get(ctx, client.ObjectKeyFromObject(want), cur)
	if err == nil {
		phase, _, _ := unstructured.NestedString(cur.Object, "status", "phase")
		if phase != "failed" {
			return nil
		}
		log.FromContext(ctx).Info("first base backup failed; trying again", "backup", cur.GetName())
		if err := r.Delete(ctx, cur); client.IgnoreNotFound(err) != nil {
			return err
		}
		return nil // created again on the next reconcile, once it's gone
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(db, want, r.Scheme()); err != nil {
		return err
	}
	return r.Create(ctx, want)
}

// block stops and says why: a person has to decide.
func (r *DatabaseReconciler) block(ctx context.Context, db *v1.Database, reason string) (ctrl.Result, error) {
	log.FromContext(ctx).Info("blocked", "reason", reason)
	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type: "Blocked", Status: metav1.ConditionTrue, Reason: ReasonBlocked,
		Message:            reason + ". To restore a generation: kubectl annotate database " + db.Name + " " + v1.RecoverFromAnnotation + "=<generation>",
		ObservedGeneration: db.Generation,
	})
	return ctrl.Result{RequeueAfter: resync}, r.setStatus(ctx, db, v1.PhaseBlocked, metav1.ConditionFalse, ReasonBlocked, reason)
}

// finalize deals with the backup bucket of a deleted Database (the
// Cluster and the rest go with it, by owner reference) and the app's
// credentials if nothing else needs them.
func (r *DatabaseReconciler) finalize(ctx context.Context, db *v1.Database, tenant bool) error {
	if !controllerutil.ContainsFinalizer(db, DatabaseFinalizer) {
		return nil
	}
	if tenant {
		if db.Spec.ReclaimPolicy == v1.Delete {
			if bucket, err := naming.Bucket(db.Namespace, naming.DatabaseBucket); err == nil {
				if err := r.Storage.DeleteBucket(ctx, bucket); err != nil {
					return err
				}
				log.FromContext(ctx).Info("deleted the backups", "bucket", bucket)
			}
		}
		if err := r.Identity.ReleaseIfUnused(ctx, db.Namespace); err != nil {
			return err
		}
	}
	deleteDatabaseMetrics(db.Namespace)
	controllerutil.RemoveFinalizer(db, DatabaseFinalizer)
	return r.Update(ctx, db)
}

func (r *DatabaseReconciler) setStatus(ctx context.Context, db *v1.Database, phase v1.DatabasePhase, ready metav1.ConditionStatus, reason, msg string) error {
	if phase != "" {
		db.Status.Phase = phase
	}
	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: ready, Reason: reason, Message: msg, ObservedGeneration: db.Generation,
	})
	db.Status.ObservedGeneration = db.Generation
	setDatabaseMetrics(db.Namespace, db.Status.Phase, db.Status.Generation)
	return r.Status().Update(ctx, db)
}

func (r *DatabaseReconciler) namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ns, nil
}

func (r *DatabaseReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(clusterGVK)
	return ctrl.NewControllerManagedBy(mgr).
		// Spec changes, deletions, and the recover-from annotation.
		For(&v1.Database{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		// The Cluster becoming ready, or disappearing.
		Owns(c).
		Named("database").
		Complete(r)
}

// setDefaults fills what the API server's defaults would, for objects
// that didn't go through it (tests, older objects).
func setDefaults(db *v1.Database) {
	if db.Spec.Instances == 0 {
		db.Spec.Instances = 1
	}
	if db.Spec.Size.IsZero() {
		db.Spec.Size = resource.MustParse("1Gi")
	}
	if db.Spec.Backup.Schedule == "" {
		db.Spec.Backup.Schedule = "0 0 5 * * *"
	}
	if db.Spec.Backup.Retention == "" {
		db.Spec.Backup.Retention = "7d"
	}
	if db.Spec.ReclaimPolicy == "" {
		db.Spec.ReclaimPolicy = v1.Retain
	}
}

func recoverFromAnnotation(db *v1.Database) (int64, error) {
	s, ok := db.Annotations[v1.RecoverFromAnnotation]
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q: expected a generation number", v1.RecoverFromAnnotation, s)
	}
	return n, nil
}

func parseServerName(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "db-"), 10, 64)
	if !strings.HasPrefix(s, "db-") || err != nil || n < 1 {
		return 0, fmt.Errorf("archives to %q, not to a generation folder db-<n>", s)
	}
	return n, nil
}

// badMarker: the marker exists but can't be read. Blocks.
type badMarker struct{ error }

func (b badMarker) Unwrap() error { return b.error }

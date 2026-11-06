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
	if db.Annotations[v1.RestoreToAnnotation] != "" {
		return r.restoreTo(ctx, db, bucket, c)
	}
	return r.update(ctx, db, bucket, c)
}

// restoreTo starts a restore to a point in time (RestoreToAnnotation): it
// plans it in the marker, then takes the Cluster down. create, called once
// the Cluster is gone, does the restore from the plan, so a controller
// restart halfway through carries on with the same restore.
func (r *DatabaseReconciler) restoreTo(ctx context.Context, db *v1.Database, bucket string, c *unstructured.Unstructured) (ctrl.Result, error) {
	t, err := restoreToAnnotation(db, r.now())
	if err != nil {
		return r.refuseRestore(ctx, db, err.Error(), leftAsItWas)
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
	planned := m != nil && m.State == StateRestoring && m.TargetTime != nil && m.TargetTime.Equal(t)
	if !planned {
		current, err := parseServerName(clusterServerName(c))
		if err != nil {
			return r.block(ctx, db, fmt.Sprintf("Cluster %s: %v", db.Name, err))
		}
		if m != nil {
			current = max(current, m.Generation)
		}
		if why, err := r.planRestore(ctx, db, bucket, t, current); err != nil {
			return ctrl.Result{}, err
		} else if why != "" {
			return r.refuseRestore(ctx, db, why, leftAsItWas)
		}
	}
	if c.GetDeletionTimestamp() == nil {
		if err := r.Delete(ctx, c); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, r.setStatus(ctx, db, v1.PhaseRestoring, metav1.ConditionFalse, ReasonInProgress,
		fmt.Sprintf("taking Cluster %s down to restore it to %s", db.Name, t.UTC().Format(time.RFC3339)))
}

// planRestore plans a restore to t in the marker: from the newest
// generation up to current with a base backup from before t, into
// generation current+1. It returns why it can't, if it can't.
func (r *DatabaseReconciler) planRestore(ctx context.Context, db *v1.Database, bucket string, t time.Time, current int64) (string, error) {
	src, ok, err := r.sourceFor(ctx, bucket, current, t)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("can't restore to %s: no finished base backup from before then (backups are kept for %s)",
			t.UTC().Format(time.RFC3339), db.Spec.Backup.Retention), nil
	}
	next := Marker{Generation: current + 1, State: StateRestoring, RestoredFrom: src, TargetTime: &t, Updated: r.now()}
	if err := r.Objects.PutObject(ctx, bucket, markerKey, next.json()); err != nil {
		return "", err
	}
	log.FromContext(ctx).Info("restore to a point in time planned", "target", t, "from", src, "generation", next.Generation)
	return "", nil
}

// What refuseRestore says happens to the database instead.
const (
	leftAsItWas    = "the database was left as it was"
	restoredLatest = "the database is restored to its latest state instead"
)

// refuseRestore drops a restore-to it can't do, and says why in the
// Restore condition (its own, so the next reconcile doesn't overwrite it),
// with what happens instead (then).
func (r *DatabaseReconciler) refuseRestore(ctx context.Context, db *v1.Database, why, then string) (ctrl.Result, error) {
	log.FromContext(ctx).Info("restore refused", "reason", why)
	patch := client.MergeFrom(db.DeepCopy())
	delete(db.Annotations, v1.RestoreToAnnotation)
	if err := r.Patch(ctx, db, patch); err != nil {
		return ctrl.Result{}, err
	}
	setDefaults(db)
	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type: "Restore", Status: metav1.ConditionFalse, Reason: "Refused",
		Message: why + "; " + then, ObservedGeneration: db.Generation,
	})
	return ctrl.Result{Requeue: true}, r.Status().Update(ctx, db)
}

// sourceFor finds the generation to restore time t from: the newest one up
// to newest with a finished base backup that ended before t.
func (r *DatabaseReconciler) sourceFor(ctx context.Context, bucket string, newest int64, t time.Time) (int64, bool, error) {
	for g := newest; g >= 1; g-- {
		ends, err := r.baseBackups(ctx, bucket, g)
		if err != nil {
			return 0, false, err
		}
		for _, end := range ends {
			if !end.IsZero() && !end.After(t) {
				return g, true, nil
			}
		}
	}
	return 0, false, nil
}

// create brings a missing Cluster back: a new database, or a restore.
func (r *DatabaseReconciler) create(ctx context.Context, db *v1.Database, bucket string) (ctrl.Result, error) {
	recoverFrom, err := recoverFromAnnotation(db)
	if err != nil {
		return r.block(ctx, db, err.Error())
	}
	if db.Annotations[v1.RestoreToAnnotation] != "" {
		if res, refused, err := r.restoreToGone(ctx, db, bucket, recoverFrom); refused || err != nil {
			return res, err
		}
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
		// The target too: a retry of an interrupted restore to a point in
		// time must stop at the same time, not at the latest.
		m.State, m.RestoredFrom, m.TargetTime = StateRestoring, p.From, p.Target
		phase = v1.PhaseRestoring
	}
	if err := r.Objects.PutObject(ctx, bucket, markerKey, m.json()); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info(p.Reason, "generation", p.Generation, "from", p.From)

	if err := r.apply(ctx, db, objectStore(db, bucket, r.Endpoint)); err != nil {
		return ctrl.Result{}, err
	}
	c := cluster(db, r.Image, r.StorageClass, p.Generation, p.From, p.Target)
	if err := controllerutil.SetControllerReference(db, c, r.Scheme()); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, c); err != nil {
		return ctrl.Result{}, err
	}
	// What was asked for is done (the marker carries it from here).
	if _, a := db.Annotations[v1.RecoverFromAnnotation]; a || db.Annotations[v1.RestoreToAnnotation] != "" {
		patch := client.MergeFrom(db.DeepCopy())
		delete(db.Annotations, v1.RecoverFromAnnotation)
		delete(db.Annotations, v1.RestoreToAnnotation)
		if err := r.Patch(ctx, db, patch); err != nil {
			return ctrl.Result{}, err
		}
		setDefaults(db)
	}

	db.Status.Generation, db.Status.RestoredFrom = p.Generation, p.From
	db.Status.RestoredTo = nil
	if p.Target != nil {
		db.Status.RestoredTo = &metav1.Time{Time: *p.Target}
	}
	if p.Action == actRestore {
		meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
			Type: "Restore", Status: metav1.ConditionTrue, Reason: "Restored",
			Message: p.Reason + fmt.Sprintf(" into generation %d", p.Generation), ObservedGeneration: db.Generation,
		})
	}
	meta.RemoveStatusCondition(&db.Status.Conditions, "Blocked")
	return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, db, phase, metav1.ConditionFalse, ReasonInProgress, p.Reason)
}

// restoreToGone handles a restore-to when the Cluster is already gone:
// unless restoreTo planned it (and took the Cluster down), it plans it
// here, so decide restores to that time and not to the latest state. One
// it can't do is refused (refused true); the next reconcile then brings the
// database back as it would without it.
func (r *DatabaseReconciler) restoreToGone(ctx context.Context, db *v1.Database, bucket string, recoverFrom int64) (ctrl.Result, bool, error) {
	refuse := func(why string) (ctrl.Result, bool, error) {
		res, err := r.refuseRestore(ctx, db, why, restoredLatest)
		return res, true, err
	}
	t, err := restoreToAnnotation(db, r.now())
	if err != nil {
		return refuse(err.Error())
	}
	if recoverFrom > 0 {
		return refuse(fmt.Sprintf("%s is set too: recovering that generation instead", v1.RecoverFromAnnotation))
	}
	data, found, err := r.Objects.GetObject(ctx, bucket, markerKey)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if !found {
		return refuse(fmt.Sprintf("can't restore to %s: there are no backups", t.UTC().Format(time.RFC3339)))
	}
	m, err := parseMarker(data)
	if err != nil {
		res, err := r.block(ctx, db, fmt.Sprintf("%s in %s: %v", markerKey, bucket, err))
		return res, true, err
	}
	if m.State == StateRestoring && m.TargetTime != nil && m.TargetTime.Equal(t) {
		return ctrl.Result{}, false, nil // planned already
	}
	why, err := r.planRestore(ctx, db, bucket, t, m.Generation)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if why != "" {
		return refuse(why)
	}
	return ctrl.Result{}, false, nil
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
	want := cluster(db, r.Image, r.StorageClass, 0, 0, nil)
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

// hasBase reports whether generation gen has a finished base backup.
func (r *DatabaseReconciler) hasBase(ctx context.Context, bucket string, gen int64) (bool, error) {
	ends, err := r.baseBackups(ctx, bucket, gen)
	return len(ends) > 0, err
}

// baseBackups lists when generation gen's finished base backups ended:
// barman's backup.info files that say status=DONE (a zero time if its
// end_time can't be read).
func (r *DatabaseReconciler) baseBackups(ctx context.Context, bucket string, gen int64) ([]time.Time, error) {
	keys, err := r.Objects.Keys(ctx, bucket, serverName(gen)+"/base/", 1000)
	if err != nil {
		return nil, err
	}
	var ends []time.Time
	for _, k := range keys {
		if !strings.HasSuffix(k, "/backup.info") {
			continue
		}
		data, found, err := r.Objects.GetObject(ctx, bucket, k)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		info := parseBackupInfo(data)
		if info["status"] != "DONE" {
			continue
		}
		end, _ := parseBarmanTime(info["end_time"])
		ends = append(ends, end)
	}
	return ends, nil
}

// parseBackupInfo reads barman's backup.info: key=value lines.
func parseBackupInfo(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// parseBarmanTime reads a backup.info time such as
// "2026-11-05 07:44:41.123456+00:00".
func parseBarmanTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05-07:00", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
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

// restoreToAnnotation reads RestoreToAnnotation: an RFC 3339 time in the
// past.
func restoreToAnnotation(db *v1.Database, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(db.Annotations[v1.RestoreToAnnotation])
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s=%q: expected a time like 2026-11-05T10:00:00Z", v1.RestoreToAnnotation, s)
	}
	if !t.Before(now) {
		return time.Time{}, fmt.Errorf("%s=%q is in the future", v1.RestoreToAnnotation, s)
	}
	return t, nil
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

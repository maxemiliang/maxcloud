package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
)

// BranchReconciler turns a DatabaseBranch into a Cluster restored from its
// database's backups. It reads the backups the way the DatabaseReconciler
// does (DB), and never writes to the database's bucket: a branch has no
// marker and archives nothing.
type BranchReconciler struct {
	client.Client
	DB *DatabaseReconciler
}

// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=databasebranches,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=databasebranches/status,verbs=get;update;patch

func branchClusterName(b *v1.DatabaseBranch) string { return "db-" + b.Name }

func (r *BranchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b v1.DatabaseBranch
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // the Cluster goes with it (owner reference)
	}
	if b.Spec.Database == "" {
		b.Spec.Database = "db"
	}
	tenant, err := isTenant(ctx, r.Client, b.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !tenant {
		return ctrl.Result{}, r.setStatus(ctx, &b, v1.BranchFailed, metav1.ConditionFalse, ReasonNotTenant,
			fmt.Sprintf("namespace %s isn't an app on the run platform", b.Namespace))
	}
	b.Status.Cluster = branchClusterName(&b)

	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(clusterGVK)
	err = r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Status.Cluster}, c)
	if err == nil {
		if !metav1.IsControlledBy(c, &b) {
			return ctrl.Result{}, r.setStatus(ctx, &b, v1.BranchFailed, metav1.ConditionFalse, ReasonBlocked,
				fmt.Sprintf("a Cluster %s already exists and isn't this branch's", b.Status.Cluster))
		}
		if clusterReady(c) {
			return ctrl.Result{}, r.setStatus(ctx, &b, v1.BranchReady, metav1.ConditionTrue, ReasonReady,
				fmt.Sprintf("Cluster %s is ready; connection details in the Secret %s-app", b.Status.Cluster, b.Status.Cluster))
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setStatus(ctx, &b, v1.BranchCreating, metav1.ConditionFalse, ReasonInProgress,
			"restoring into Cluster "+b.Status.Cluster)
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if b.Status.Phase == v1.BranchReady || b.Status.Phase == v1.BranchCreating && b.Status.FromGeneration > 0 {
		// It was made once and is gone now: a branch isn't brought back.
		return ctrl.Result{}, r.setStatus(ctx, &b, v1.BranchFailed, metav1.ConditionFalse, "ClusterGone",
			"Cluster "+b.Status.Cluster+" was deleted; delete this branch and make a new one")
	}

	var db v1.Database
	if err := r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Spec.Database}, &db); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, &b, v1.BranchFailed, metav1.ConditionFalse, "NoDatabase",
				fmt.Sprintf("no Database %s in %s (db: true in the app's maxcloud.yaml)", b.Spec.Database, b.Namespace))
		}
		return ctrl.Result{}, err
	}
	setDefaults(&db)
	if db.Status.Bucket == "" || db.Status.Generation == 0 {
		return ctrl.Result{RequeueAfter: time.Minute}, r.setStatus(ctx, &b, v1.BranchCreating, metav1.ConditionFalse, ReasonInProgress,
			"waiting for Database "+db.Name+" to have backups")
	}

	at := time.Now()
	if b.Spec.At != nil {
		at = b.Spec.At.Time
	}
	gen, ok, err := r.DB.sourceFor(ctx, db.Status.Bucket, db.Status.Generation, at)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		return ctrl.Result{}, r.setStatus(ctx, &b, v1.BranchFailed, metav1.ConditionFalse, "NoBackupBefore",
			fmt.Sprintf("no finished base backup of %s from before %s", db.Name, at.UTC().Format(time.RFC3339)))
	}
	var target *time.Time
	if b.Spec.At != nil {
		target = &b.Spec.At.Time
	}

	want := branchCluster(&db, &b, r.DB.Image, r.DB.StorageClass, gen, target)
	if err := controllerutil.SetControllerReference(&b, want, r.Scheme()); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, want); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("branch restoring", "cluster", want.GetName(), "from", gen, "at", target)
	b.Status.FromGeneration = gen
	return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setStatus(ctx, &b, v1.BranchCreating, metav1.ConditionFalse, ReasonInProgress,
		fmt.Sprintf("restoring generation %d of %s into Cluster %s", gen, db.Name, want.GetName()))
}

// branchCluster is the database's Cluster restored under the branch's
// name: one instance, no WAL archiving, no PDB.
func branchCluster(db *v1.Database, b *v1.DatabaseBranch, image, storageClass string, gen int64, target *time.Time) *unstructured.Unstructured {
	src := db.DeepCopy()
	src.Spec.Instances = 1
	if b.Spec.Size != nil {
		src.Spec.Size = *b.Spec.Size
	}
	u := cluster(src, image, storageClass, 0, gen, target)
	u.SetName(branchClusterName(b))
	unstructured.RemoveNestedField(u.Object, "spec", "plugins")
	_ = unstructured.SetNestedField(u.Object, false, "spec", "enablePDB")
	return u
}

func (r *BranchReconciler) setStatus(ctx context.Context, b *v1.DatabaseBranch, phase v1.BranchPhase, ready metav1.ConditionStatus, reason, msg string) error {
	b.Status.Phase = phase
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: ready, Reason: reason, Message: msg, ObservedGeneration: b.Generation,
	})
	b.Status.ObservedGeneration = b.Generation
	return r.Status().Update(ctx, b)
}

func (r *BranchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(clusterGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.DatabaseBranch{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(c).
		Named("databasebranch").
		Complete(r)
}

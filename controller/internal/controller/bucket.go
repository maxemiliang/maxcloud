// Package controller has the reconcilers: Bucket, Database, DatabaseBranch
// and Domain, and the loops that keep the registry, secrets, image pull
// credentials and zot's rules in line.
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
	"github.com/maxemiliang/maxcloud/controller/internal/s3policy"
)

const (
	// BucketFinalizer holds a Bucket until its storage is cleaned up.
	BucketFinalizer = "maxcloud.gronblom.dev/bucket"

	// TenantLabel marks the namespaces of apps on the run platform (the app
	// registry sets it). The controller acts in no other namespace.
	TenantLabel = "run.gronblom.dev/tenant"

	// ManagedByLabel marks the Secrets the controller writes.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "maxcloud-controller"

	// How often a ready Bucket is looked at again: its metrics, and
	// anything deleted by hand (the credentials Secret).
	resync = 5 * time.Minute

	// How soon to look again when the app's entry is about to get its
	// registration ID.
	registrationWait = 5 * time.Second
)

// Condition reasons.
const (
	ReasonReady      = "Ready"
	ReasonNotTenant  = "NotATenant"
	ReasonBadName    = "InvalidName"
	ReasonAPIError   = "APIError"
	ReasonInProgress = "Reconciling"
)

// BucketReconciler gives an app the buckets the registry asks for, and the
// credentials to use them.
//
// The credentials belong to the app, not to one bucket (see Identity).
type BucketReconciler struct {
	client.Client
	Identity *Identity
	Storage  objectstorage.Storage
	Policies objectstorage.BucketPolicies
	Metrics  *MetricsCache
	// Routes gives public buckets a hostname; nil or disabled: none.
	Routes *PublicRoutes
	// Endpoint of the Object Storage instance, shown in the status.
	Endpoint string
	// Registrations: only the registration the app's data belongs to gets
	// its buckets (nil: no check).
	Registrations *Registrations
}

// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=buckets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=buckets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete

func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b v1.Bucket
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	app := b.Namespace

	tenant, err := isTenant(ctx, r.Client, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !b.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &b, tenant)
	}
	if !tenant {
		return ctrl.Result{}, r.setReady(ctx, &b, metav1.ConditionFalse, ReasonNotTenant,
			fmt.Sprintf("namespace %s isn't an app on the run platform (no %s label)", app, TenantLabel))
	}

	bucket, err := naming.Bucket(app, b.Name)
	if err != nil {
		return ctrl.Result{}, r.setReady(ctx, &b, metav1.ConditionFalse, ReasonBadName, err.Error())
	}

	// Before anything is made or handed out: the app's credentials reach
	// every bucket mc-<app>--*, not only this one.
	v, err := r.Registrations.Check(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if v.Wait {
		return ctrl.Result{RequeueAfter: registrationWait}, nil
	}
	if !v.Allowed {
		return ctrl.Result{RequeueAfter: resync}, r.setReady(ctx, &b, metav1.ConditionFalse, ReasonRegistration, v.Reason)
	}

	if controllerutil.AddFinalizer(&b, BucketFinalizer) {
		if err := r.Update(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcile(ctx, &b, app, bucket); err != nil {
		if serr := r.setReady(ctx, &b, metav1.ConditionFalse, ReasonAPIError, err.Error()); serr != nil {
			return ctrl.Result{}, errors.Join(err, serr)
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resync}, nil
}

func (r *BucketReconciler) reconcile(ctx context.Context, b *v1.Bucket, app, bucket string) error {
	if err := r.Identity.Ensure(ctx, app); err != nil {
		return err
	}
	if err := r.Storage.EnsureBucket(ctx, bucket); err != nil {
		return err
	}

	// The bucket policy only changes with the spec: skip the S3 call when
	// this generation is already done.
	ready := meta.FindStatusCondition(b.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || b.Status.ObservedGeneration != b.Generation {
		var err error
		if b.Spec.Public {
			err = r.Policies.PutBucketPolicy(ctx, bucket, s3policy.PublicRead(bucket).JSON())
		} else {
			err = r.Policies.DeleteBucketPolicy(ctx, bucket)
		}
		if err != nil {
			return err
		}
	}

	b.Status.URL = ""
	if r.Routes.Enabled() {
		if b.Spec.Public {
			host := r.Routes.Hostname(app, b.Name)
			if err := r.Routes.Ensure(ctx, bucket, host); err != nil {
				return err
			}
			b.Status.URL = "https://" + host + "/"
		} else if err := r.Routes.Remove(ctx, bucket); err != nil {
			return err
		}
	}

	b.Status.BucketName = bucket
	b.Status.Endpoint = r.Endpoint
	if m, ok, err := r.Metrics.Get(ctx, bucket); err != nil {
		log.FromContext(ctx).Error(err, "bucket metrics") // not worth failing for
	} else if ok {
		b.Status.SizeBytes = &m.SizeBytes
		b.Status.Objects = &m.Objects
		bucketSize.WithLabelValues(app, bucket).Set(float64(m.SizeBytes))
		bucketObjects.WithLabelValues(app, bucket).Set(float64(m.Objects))
	}
	return r.setReady(ctx, b, metav1.ConditionTrue, ReasonReady, "bucket "+bucket+" is ready")
}

// finalize cleans up after a deleted Bucket: the bucket itself if its
// reclaim policy says so, and the app's credentials if it was the app's
// last Bucket.
func (r *BucketReconciler) finalize(ctx context.Context, b *v1.Bucket, tenant bool) error {
	if !controllerutil.ContainsFinalizer(b, BucketFinalizer) {
		return nil
	}
	// Outside a tenant namespace nothing was ever created.
	if tenant {
		app := b.Namespace
		if r.Routes.Enabled() {
			if bucket, err := naming.Bucket(app, b.Name); err == nil {
				if err := r.Routes.Remove(ctx, bucket); err != nil {
					return err
				}
			}
		}
		if b.Spec.ReclaimPolicy == v1.Delete {
			if bucket, err := naming.Bucket(app, b.Name); err == nil {
				if err := r.Storage.DeleteBucket(ctx, bucket); err != nil {
					return err
				}
				bucketSize.DeleteLabelValues(app, bucket)
				bucketObjects.DeleteLabelValues(app, bucket)
				log.FromContext(ctx).Info("deleted bucket", "bucket", bucket)
			}
		}
		if err := r.Identity.ReleaseIfUnused(ctx, app); err != nil {
			return err
		}
	}
	controllerutil.RemoveFinalizer(b, BucketFinalizer)
	return r.Update(ctx, b)
}

func (r *BucketReconciler) setReady(ctx context.Context, b *v1.Bucket, status metav1.ConditionStatus, reason, msg string) error {
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: b.Generation,
	})
	b.Status.ObservedGeneration = b.Generation
	return r.Status().Update(ctx, b)
}

func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Only spec changes and deletions (both bump the generation), not
		// the controller's own status writes; the resync covers the rest.
		For(&v1.Bucket{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("bucket").
		Complete(r)
}

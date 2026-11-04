// Package controller has the reconcilers: Bucket (and later Database).
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// The credentials belong to the app, not to one bucket: an S3 user mc-<app>
// whose policy covers every bucket mc-<app>--*, and the Secret maxcloud-s3
// with its key. The first Bucket in a namespace creates them; the last one
// to go removes them.
type BucketReconciler struct {
	client.Client
	// APIReader reads Secrets straight from the API server, so the
	// controller doesn't cache every Secret in the cluster.
	APIReader client.Reader

	Storage  objectstorage.Storage
	Policies objectstorage.BucketPolicies
	Metrics  *MetricsCache

	// Endpoint and Region of the Object Storage instance, written into the
	// apps' Secrets.
	Endpoint string
	Region   string
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

	tenant, err := r.isTenant(ctx, app)
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
	if err := r.ensureIdentity(ctx, app); err != nil {
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

// ensureIdentity makes sure the app has its S3 user, its policy, and a
// Secret with a working access key.
func (r *BucketReconciler) ensureIdentity(ctx context.Context, app string) error {
	user := naming.User(app)
	if err := r.Storage.EnsureUser(ctx, user); err != nil {
		return err
	}
	desc := fmt.Sprintf("App %s on maxcloud: its own buckets (%s*), managed by maxcloud-controller.", app, naming.BucketPrefix(app))
	if err := r.Storage.EnsurePolicy(ctx, user, desc, s3policy.App(app).Encoded()); err != nil {
		return err
	}
	if err := r.Storage.EnsureUserPolicy(ctx, user, user); err != nil {
		return err
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: app, Name: naming.SecretName}
	err := r.APIReader.Get(ctx, key, &secret)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	keys, err := r.Storage.AccessKeys(ctx, user)
	if err != nil {
		return err
	}
	id, secretKey := string(secret.Data["AWS_ACCESS_KEY_ID"]), string(secret.Data["AWS_SECRET_ACCESS_KEY"])
	if id == "" || secretKey == "" || !contains(keys, id) {
		// The Secret is gone or its key doesn't exist any more. A key's
		// secret part can't be read back, so the others are useless:
		// delete them and make a new one.
		for _, k := range keys {
			if err := r.Storage.DeleteAccessKey(ctx, user, k); err != nil {
				return err
			}
		}
		if id, secretKey, err = r.Storage.CreateAccessKey(ctx, user); err != nil {
			return err
		}
		log.FromContext(ctx).Info("new access key", "app", app, "user", user, "accessKeyID", id)
	}

	want := r.secretData(app, id, secretKey)
	if exists && sameData(secret.Data, want) && secret.Labels[ManagedByLabel] == managedBy {
		return nil
	}
	secret.Namespace, secret.Name = app, naming.SecretName
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[ManagedByLabel] = managedBy
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = want
	if exists {
		return r.Update(ctx, &secret)
	}
	return r.Create(ctx, &secret)
}

// secretData is the app's Secret: environment variables every AWS SDK
// reads, so `envFrom: [{secretRef: {name: maxcloud-s3}}]` is all an app
// needs, plus MAXCLOUD_APP, from which the SDKs' bucketName("uploads")
// makes mc-<app>--uploads.
func (r *BucketReconciler) secretData(app, id, secret string) map[string][]byte {
	return map[string][]byte{
		"MAXCLOUD_APP":          []byte(app),
		"AWS_ACCESS_KEY_ID":     []byte(id),
		"AWS_SECRET_ACCESS_KEY": []byte(secret),
		"AWS_ENDPOINT_URL_S3":   []byte(r.Endpoint),
		"AWS_REGION":            []byte(r.Region),
		// UpCloud rejects the checksums newer AWS SDKs send by default.
		"AWS_REQUEST_CHECKSUM_CALCULATION": []byte("when_required"),
		"AWS_RESPONSE_CHECKSUM_VALIDATION": []byte("when_required"),
	}
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
		last, err := r.lastBucket(ctx, b)
		if err != nil {
			return err
		}
		if last {
			if err := r.removeIdentity(ctx, app); err != nil {
				return err
			}
		}
	}
	controllerutil.RemoveFinalizer(b, BucketFinalizer)
	return r.Update(ctx, b)
}

// lastBucket reports whether no other Bucket in b's namespace still needs
// the app's credentials.
func (r *BucketReconciler) lastBucket(ctx context.Context, b *v1.Bucket) (bool, error) {
	var list v1.BucketList
	if err := r.List(ctx, &list, client.InNamespace(b.Namespace)); err != nil {
		return false, err
	}
	for _, o := range list.Items {
		if o.Name != b.Name && o.DeletionTimestamp.IsZero() {
			return false, nil
		}
	}
	return true, nil
}

func (r *BucketReconciler) removeIdentity(ctx context.Context, app string) error {
	user := naming.User(app)
	if err := r.Storage.DeleteUser(ctx, user); err != nil {
		return err
	}
	if err := r.Storage.DeletePolicy(ctx, user); err != nil {
		return err
	}
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: app, Name: naming.SecretName}}
	if err := r.Delete(ctx, &secret); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.FromContext(ctx).Info("removed the app's credentials", "app", app, "user", user)
	return nil
}

func (r *BucketReconciler) isTenant(ctx context.Context, namespace string) (bool, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return ns.Labels[TenantLabel] == "true", nil
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

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func sameData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if string(b[k]) != string(v) {
			return false
		}
	}
	return true
}

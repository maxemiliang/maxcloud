package controller

import (
	"context"
	"fmt"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
	"github.com/maxemiliang/maxcloud/controller/internal/s3policy"
)

// Identity is an app's access to Object Storage: an S3 user mc-<app> whose
// policy covers every bucket mc-<app>--*, and the Secret maxcloud-s3 in the
// app's namespace with its key. It belongs to the app, not to one Bucket or
// Database: the first of them creates it, the last one to go removes it.
type Identity struct {
	Client client.Client
	// APIReader reads Secrets straight from the API server, so the
	// controller doesn't cache every Secret in the cluster.
	APIReader client.Reader
	Storage   objectstorage.Storage

	// Endpoint and Region of the Object Storage instance, for the Secret.
	Endpoint string
	Region   string

	// Buckets and Databases reconcile in parallel; one app's identity is
	// made or removed by one of them at a time.
	mu sync.Mutex
}

// Ensure makes sure the app has its S3 user, its policy, and a Secret with
// a working access key.
func (i *Identity) Ensure(ctx context.Context, app string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	user := naming.User(app)
	if err := i.Storage.EnsureUser(ctx, user); err != nil {
		return err
	}
	desc := fmt.Sprintf("App %s on maxcloud: its own buckets (%s*), managed by maxcloud-controller.", app, naming.BucketPrefix(app))
	if err := i.Storage.EnsurePolicy(ctx, user, desc, s3policy.App(app).Encoded()); err != nil {
		return err
	}
	if err := i.Storage.EnsureUserPolicy(ctx, user, user); err != nil {
		return err
	}

	var secret corev1.Secret
	err := i.APIReader.Get(ctx, types.NamespacedName{Namespace: app, Name: naming.SecretName}, &secret)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	keys, err := i.Storage.AccessKeys(ctx, user)
	if err != nil {
		return err
	}
	id, secretKey := string(secret.Data["AWS_ACCESS_KEY_ID"]), string(secret.Data["AWS_SECRET_ACCESS_KEY"])
	if id == "" || secretKey == "" || !slices.Contains(keys, id) {
		// The Secret is gone or its key doesn't exist any more. A key's
		// secret part can't be read back, so the others are useless:
		// delete them and make a new one.
		for _, k := range keys {
			if err := i.Storage.DeleteAccessKey(ctx, user, k); err != nil {
				return err
			}
		}
		if id, secretKey, err = i.Storage.CreateAccessKey(ctx, user); err != nil {
			return err
		}
		log.FromContext(ctx).Info("new access key", "app", app, "user", user, "accessKeyID", id)
	}

	want := i.secretData(app, id, secretKey)
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
		return i.Client.Update(ctx, &secret)
	}
	return i.Client.Create(ctx, &secret)
}

// secretData is the app's Secret: environment variables every AWS SDK
// reads, so `envFrom: [{secretRef: {name: maxcloud-s3}}]` is all an app
// needs, plus MAXCLOUD_APP, from which the SDKs' bucketName("uploads")
// makes mc-<app>--uploads. The database's ObjectStore reads its keys too.
func (i *Identity) secretData(app, id, secret string) map[string][]byte {
	return map[string][]byte{
		"MAXCLOUD_APP":          []byte(app),
		"AWS_ACCESS_KEY_ID":     []byte(id),
		"AWS_SECRET_ACCESS_KEY": []byte(secret),
		"AWS_ENDPOINT_URL_S3":   []byte(i.Endpoint),
		"AWS_REGION":            []byte(i.Region),
		// UpCloud rejects the checksums newer AWS SDKs send by default.
		"AWS_REQUEST_CHECKSUM_CALCULATION": []byte("when_required"),
		"AWS_RESPONSE_CHECKSUM_VALIDATION": []byte("when_required"),
	}
}

// ReleaseIfUnused removes the app's identity when no Bucket or Database in
// its namespace needs it any more. Objects being deleted don't count, so
// the caller (finalizing one) can call it before removing its finalizer.
func (i *Identity) ReleaseIfUnused(ctx context.Context, app string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	var buckets v1.BucketList
	if err := i.Client.List(ctx, &buckets, client.InNamespace(app)); err != nil {
		return err
	}
	for _, o := range buckets.Items {
		if o.DeletionTimestamp.IsZero() {
			return nil
		}
	}
	var dbs v1.DatabaseList
	if err := i.Client.List(ctx, &dbs, client.InNamespace(app)); err != nil {
		return err
	}
	for _, o := range dbs.Items {
		if o.DeletionTimestamp.IsZero() {
			return nil
		}
	}

	user := naming.User(app)
	if err := i.Storage.DeleteUser(ctx, user); err != nil {
		return err
	}
	if err := i.Storage.DeletePolicy(ctx, user); err != nil {
		return err
	}
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: app, Name: naming.SecretName}}
	if err := i.Client.Delete(ctx, &secret); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.FromContext(ctx).Info("removed the app's credentials", "app", app, "user", user)
	return nil
}

// isTenant reports whether a namespace is an app on the run platform.
func isTenant(ctx context.Context, c client.Client, namespace string) (bool, error) {
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return ns.Labels[TenantLabel] == "true", nil
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

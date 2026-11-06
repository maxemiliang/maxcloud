package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// An app's secrets (mcloud secrets set) live only in the cluster, in the
// Secret app-secrets in its namespace. SecretBackup keeps an age-encrypted
// copy of each in the registry's bucket, and puts the Secret back from it
// when it's missing: after a cluster rebuild, or deleted by hand.

const (
	// AppSecretsName is the Secret mcloud keeps an app's secrets in.
	AppSecretsName = "app-secrets"
	secretsPrefix  = "secrets/"
	// mcloud sets this on app-secrets so a GitOps app's Kustomization
	// doesn't prune it; a restored Secret gets it too.
	pruneAnnotation = "kustomize.toolkit.fluxcd.io/prune"
)

// SecretBackup copies each app's app-secrets to a bucket, encrypted, and
// restores missing ones.
type SecretBackup struct {
	client.Client
	Storage objectstorage.Storage
	Objects objectstorage.Objects
	Bucket  string
	// Identity decrypts the copies; they're encrypted to its recipient.
	Identity *age.X25519Identity
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete

func secretsKey(app string) string { return secretsPrefix + app + ".age" }

func isAppSecrets(o client.Object) bool {
	return o.GetName() == AppSecretsName && o.GetLabels()[ManagedByLabel] == mcloudManaged
}

// Reconcile handles the app-secrets of one namespace: a copy of the Secret
// if it's there, the Secret from its copy if not.
func (r *SecretBackup) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	app := req.Namespace
	if ok, err := r.liveTenant(ctx, app); err != nil || !ok {
		return ctrl.Result{}, err
	}
	var s corev1.Secret
	err := r.Get(ctx, req.NamespacedName, &s)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.restore(ctx, app)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !isAppSecrets(&s) || !s.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.backup(ctx, &s)
}

// liveTenant: the namespace exists, is an app's, and isn't going away.
func (r *SecretBackup) liveTenant(ctx context.Context, name string) (bool, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return ns.Labels[TenantLabel] == "true" && ns.DeletionTimestamp.IsZero(), nil
}

// inRegistry: the app has a registry entry that isn't being deleted. Only
// then is a copy made: an app on its way out (its entry deleted, which
// deletes the copy, and its namespace not gone yet) mustn't get one again.
func (r *SecretBackup) inRegistry(ctx context.Context, app string) (bool, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: app}, u); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return u.GetDeletionTimestamp().IsZero(), nil
}

// secretJSON is what's kept of the Secret: its data as {"KEY":"value"}.
func secretJSON(data map[string][]byte) ([]byte, error) {
	m := make(map[string]string, len(data))
	for k, v := range data {
		m[k] = string(v)
	}
	return json.Marshal(m) // keys come out sorted
}

func (r *SecretBackup) backup(ctx context.Context, s *corev1.Secret) error {
	app := s.Namespace
	if ok, err := r.inRegistry(ctx, app); err != nil || !ok {
		return err
	}
	plain, err := secretJSON(s.Data)
	if err != nil {
		return err
	}
	cur, found, err := r.Objects.GetObject(ctx, r.Bucket, secretsKey(app))
	if errors.Is(err, objectstorage.ErrNoSuchBucket) {
		if err := r.Storage.EnsureBucket(ctx, r.Bucket); err != nil {
			return err
		}
		found, err = false, nil
	}
	if err != nil {
		return err
	}
	if found {
		stored, err := r.decrypt(cur)
		if err == nil {
			if same, _ := secretJSON(stored); bytes.Equal(same, plain) {
				return nil
			}
		} else {
			log.FromContext(ctx).Info("app secrets' copy unreadable; writing a new one", "app", app, "reason", err.Error())
		}
	}
	enc, err := r.encrypt(plain)
	if err != nil {
		return err
	}
	if err := r.Objects.PutObject(ctx, r.Bucket, secretsKey(app), enc); err != nil {
		return err
	}
	log.FromContext(ctx).Info("app secrets copied", "app", app, "keys", len(s.Data), "bucket", r.Bucket)
	return nil
}

// restore creates the app's app-secrets from its copy, if it has one.
func (r *SecretBackup) restore(ctx context.Context, app string) error {
	enc, found, err := r.Objects.GetObject(ctx, r.Bucket, secretsKey(app))
	if errors.Is(err, objectstorage.ErrNoSuchBucket) || (err == nil && !found) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := r.decrypt(enc)
	if err != nil {
		return fmt.Errorf("%s: %w", secretsKey(app), err)
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   app,
			Name:        AppSecretsName,
			Labels:      map[string]string{ManagedByLabel: mcloudManaged},
			Annotations: map[string]string{pruneAnnotation: "disabled"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := r.Create(ctx, s); err != nil {
		if apierrors.IsAlreadyExists(err) || apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
			return nil
		}
		return err
	}
	log.FromContext(ctx).Info("app secrets restored from their copy", "app", app, "keys", len(data), "bucket", r.Bucket)
	return nil
}

// Restore puts back the app-secrets of every app that has a copy, a
// namespace, and no Secret. An app without a namespace is skipped: it may
// still be on its way back. RegistryBackup runs this after its own Restore,
// so the apps it brings back have a chance to get their namespaces first.
func (r *SecretBackup) Restore(ctx context.Context) error {
	if err := r.Storage.EnsureBucket(ctx, r.Bucket); err != nil {
		return err
	}
	keys, err := r.Objects.Keys(ctx, r.Bucket, secretsPrefix, 1000)
	if err != nil {
		return err
	}
	var errs []error
	for _, k := range keys {
		if !strings.HasSuffix(k, ".age") {
			continue
		}
		app := strings.TrimSuffix(strings.TrimPrefix(k, secretsPrefix), ".age")
		if ok, err := r.liveTenant(ctx, app); err != nil || !ok {
			errs = append(errs, err)
			continue
		}
		err := r.Get(ctx, types.NamespacedName{Namespace: app, Name: AppSecretsName}, &corev1.Secret{})
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, r.restore(ctx, app))
	}
	return errors.Join(errs...)
}

func (r *SecretBackup) encrypt(plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r.Identity.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r *SecretBackup) decrypt(enc []byte) (map[string][]byte, error) {
	rd, err := age.Decrypt(bytes.NewReader(enc), r.Identity)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("not a JSON object of strings: %w", err)
	}
	data := make(map[string][]byte, len(m))
	for k, v := range m {
		data[k] = []byte(v)
	}
	return data, nil
}

// ParseAgeIdentity reads an age X25519 identity (AGE-SECRET-KEY-1...).
func ParseAgeIdentity(s string) (*age.X25519Identity, error) {
	return age.ParseX25519Identity(strings.TrimSpace(s))
}

func (r *SecretBackup) SetupWithManager(mgr ctrl.Manager) error {
	// A tenant namespace appearing (an app coming back after a rebuild) gets
	// its Secret back without waiting for the restore loop.
	isTenant := func(o client.Object) bool { return o.GetLabels()[TenantLabel] == "true" }
	newTenant := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isTenant(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return !isTenant(e.ObjectOld) && isTenant(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	toSecret := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetName(), Name: AppSecretsName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(predicate.NewPredicateFuncs(isAppSecrets))).
		Watches(&corev1.Namespace{}, toSecret, builder.WithPredicates(newTenant)).
		Named("appsecrets").
		Complete(r)
}

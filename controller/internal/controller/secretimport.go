package controller

import (
	"context"
	"errors"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/maxemiliang/maxcloud/controller/internal/openbao"
)

// With OpenBao (MAXCLOUD_OPENBAO_ADDR), an app's secrets live at
// apps/<namespace>/app-secrets, and External Secrets Operator makes the
// Secret app-secrets from there. SecretBackup is off then: ESO owns the
// Secret, and putting it back from the age copy would fight ESO.
//
// SecretImport moves apps over, once each: for every app namespace whose
// KV secret doesn't exist yet, it writes the Secret app-secrets' data
// there (an empty secret if the namespace has no Secret), with a
// check-and-set that only creates, so it never overwrites what's in
// OpenBao. An empty KV secret is still a secret: ESO syncs it without
// an error, where a missing one is a SecretSyncedError, so every app
// namespace gets one.

// SecretKV is what SecretImport and RegistryBackup need of OpenBao's KV
// (openbao.Client).
type SecretKV interface {
	Exists(ctx context.Context, path string) (bool, error)
	Write(ctx context.Context, path string, data map[string]string, cas int) (int, error)
	DeleteMetadata(ctx context.Context, path string) error
}

// SecretKVPath is where a namespace's app secrets are, under the KV mount.
func SecretKVPath(namespace string) string { return namespace + "/" + AppSecretsName }

// SecretImport copies each app namespace's app-secrets into OpenBao if
// OpenBao has none for it yet.
type SecretImport struct {
	client.Client
	// APIReader reads the Secret uncached: one without mcloud's label (from
	// before mcloud marked them) is imported too.
	APIReader client.Reader
	KV        SecretKV
}

// Reconcile handles one namespace (req.Name).
func (r *SecretImport) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := req.Name
	// Not for an app on its way out: its entry's deletion deletes the KV
	// secret, and this mustn't make it again from the Secret that's
	// still there until the namespace goes.
	if ok, err := liveTenant(ctx, r.Client, ns); err != nil || !ok {
		return ctrl.Result{}, err
	}
	if ok, err := inRegistry(ctx, r.Client, ns); err != nil || !ok {
		return ctrl.Result{}, err
	}
	path := SecretKVPath(ns)
	exists, err := r.KV.Exists(ctx, path)
	if err != nil || exists {
		return ctrl.Result{}, err
	}
	data := map[string]string{}
	var s corev1.Secret
	err = r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: AppSecretsName}, &s)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	for k, v := range s.Data {
		if !utf8.Valid(v) {
			// KV values are JSON strings; this one would be mangled.
			log.FromContext(ctx).Error(nil, "app secret isn't text; not copied to OpenBao (set it again with mcloud secrets set)", "namespace", ns, "key", k)
			continue
		}
		data[k] = string(v)
	}
	if _, err := r.KV.Write(ctx, path, data, 0); err != nil {
		if errors.Is(err, openbao.ErrCASMismatch) {
			return ctrl.Result{}, nil // written meanwhile: it's there
		}
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("app secrets copied to OpenBao", "namespace", ns, "keys", len(data), "path", path)
	return ctrl.Result{}, nil
}

func (r *SecretImport) SetupWithManager(mgr ctrl.Manager) error {
	// Every app namespace when the controller starts (the informer's
	// creates), and one that becomes an app's later.
	isTenant := func(o client.Object) bool { return o.GetLabels()[TenantLabel] == "true" }
	tenants := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isTenant(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return !isTenant(e.ObjectOld) && isTenant(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	toNamespace := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: o.GetNamespace()}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(tenants)).
		Watches(&corev1.Secret{}, toNamespace, builder.WithPredicates(predicate.NewPredicateFuncs(isAppSecrets))).
		Named("appsecretsimport").
		Complete(r)
}

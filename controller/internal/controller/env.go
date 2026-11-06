package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// An app's plain variables set with mcloud env set live only in the
// cluster, in the ConfigMap app-env in its namespace (those in maxcloud.yaml
// are in its manifests). EnvBackup keeps a copy of each in the registry's
// bucket, as JSON (they aren't secret), and puts the ConfigMap back from it
// when it's missing: after a cluster rebuild, or deleted by hand. The same
// as SecretBackup does for app-secrets.

const (
	// AppEnvName is the ConfigMap mcloud keeps an app's variables in.
	AppEnvName = "app-env"
	envPrefix  = "env/"
)

// EnvBackup copies each app's app-env to a bucket and restores missing ones.
type EnvBackup struct {
	client.Client
	Storage objectstorage.Storage
	Objects objectstorage.Objects
	Bucket  string
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create

func envKey(app string) string { return envPrefix + app + ".json" }

func isAppEnv(o client.Object) bool {
	return o.GetName() == AppEnvName && o.GetLabels()[ManagedByLabel] == mcloudManaged
}

// envJSON is what's kept of the ConfigMap: its data as {"KEY":"value"}.
func envJSON(data map[string]string) ([]byte, error) {
	if data == nil {
		data = map[string]string{}
	}
	return json.Marshal(data) // keys come out sorted
}

// Reconcile handles the app-env of one namespace: a copy of the ConfigMap
// if it's there, the ConfigMap from its copy if not.
func (r *EnvBackup) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	app := req.Namespace
	if ok, err := liveTenant(ctx, r.Client, app); err != nil || !ok {
		return ctrl.Result{}, err
	}
	var cm corev1.ConfigMap
	err := r.Get(ctx, req.NamespacedName, &cm)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.restore(ctx, app)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !isAppEnv(&cm) || !cm.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.backup(ctx, &cm)
}

func (r *EnvBackup) backup(ctx context.Context, cm *corev1.ConfigMap) error {
	app := cm.Namespace
	if ok, err := inRegistry(ctx, r.Client, app); err != nil || !ok {
		return err
	}
	data, err := envJSON(cm.Data)
	if err != nil {
		return err
	}
	cur, found, err := r.Objects.GetObject(ctx, r.Bucket, envKey(app))
	if errors.Is(err, objectstorage.ErrNoSuchBucket) {
		if err := r.Storage.EnsureBucket(ctx, r.Bucket); err != nil {
			return err
		}
		found, err = false, nil
	}
	if err != nil {
		return err
	}
	if found && bytes.Equal(cur, data) {
		return nil
	}
	if err := r.Objects.PutObject(ctx, r.Bucket, envKey(app), data); err != nil {
		return err
	}
	log.FromContext(ctx).Info("app env copied", "app", app, "keys", len(cm.Data), "bucket", r.Bucket)
	return nil
}

// restore creates the app's app-env from its copy, if it has one.
func (r *EnvBackup) restore(ctx context.Context, app string) error {
	data, found, err := r.Objects.GetObject(ctx, r.Bucket, envKey(app))
	if errors.Is(err, objectstorage.ErrNoSuchBucket) || (err == nil && !found) {
		return nil
	}
	if err != nil {
		return err
	}
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%s: not a JSON object of strings: %w", envKey(app), err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   app,
			Name:        AppEnvName,
			Labels:      map[string]string{ManagedByLabel: mcloudManaged},
			Annotations: map[string]string{pruneAnnotation: "disabled"},
		},
		Data: env,
	}
	if err := r.Create(ctx, cm); err != nil {
		if apierrors.IsAlreadyExists(err) || apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
			return nil
		}
		return err
	}
	log.FromContext(ctx).Info("app env restored from its copy", "app", app, "keys", len(env), "bucket", r.Bucket)
	return nil
}

// Restore puts back the app-env of every app that has a copy, a namespace,
// and no ConfigMap. RegistryBackup runs it after its own Restore, like
// SecretBackup's.
func (r *EnvBackup) Restore(ctx context.Context) error {
	if err := r.Storage.EnsureBucket(ctx, r.Bucket); err != nil {
		return err
	}
	keys, err := r.Objects.Keys(ctx, r.Bucket, envPrefix, 1000)
	if err != nil {
		return err
	}
	var errs []error
	for _, k := range keys {
		if !strings.HasSuffix(k, ".json") {
			continue
		}
		app := strings.TrimSuffix(strings.TrimPrefix(k, envPrefix), ".json")
		if ok, err := liveTenant(ctx, r.Client, app); err != nil || !ok {
			errs = append(errs, err)
			continue
		}
		err := r.Get(ctx, types.NamespacedName{Namespace: app, Name: AppEnvName}, &corev1.ConfigMap{})
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

func (r *EnvBackup) SetupWithManager(mgr ctrl.Manager) error {
	isTenant := func(o client.Object) bool { return o.GetLabels()[TenantLabel] == "true" }
	newTenant := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isTenant(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return !isTenant(e.ObjectOld) && isTenant(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	toConfigMap := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetName(), Name: AppEnvName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(predicate.NewPredicateFuncs(isAppEnv))).
		Watches(&corev1.Namespace{}, toConfigMap, builder.WithPredicates(newTenant)).
		Named("appenv").
		Complete(r)
}

package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// The app registry's entries (the Flux Operator's ResourceSetInputProviders
// in flux-system) that mcloud made in the cluster, as opposed to ones in the
// homelab repo. Git keeps those safe through a cluster rebuild; for these,
// RegistryBackup keeps a copy of each in a bucket and puts back any that are
// missing.
var providerGVK = schema.GroupVersionKind{Group: "fluxcd.controlplane.io", Version: "v1", Kind: "ResourceSetInputProvider"}

const (
	RegistryNamespace = "flux-system"
	appLabel          = "run.gronblom.dev/app"
	mcloudManaged     = "mcloud"
	// RegistryFinalizer, on every app's entry, keeps it until the app's
	// copies are deleted too, so a deliberately removed app isn't put back
	// after a restart, and an app made later under the same name doesn't
	// get its secrets.
	RegistryFinalizer = "maxcloud.gronblom.dev/registry-backup"
	registryPrefix    = "apps/"
)

// RegistryBackup copies mcloud-made registry entries to a bucket and
// restores missing ones. When any app's entry is deleted, it deletes the
// app's copies (entry, secrets, variables) first.
type RegistryBackup struct {
	client.Client
	Storage objectstorage.Storage
	Objects objectstorage.Objects
	Bucket  string
	// Secrets and Env, if set, restore apps' secrets and variables after
	// each Restore of the entries.
	Secrets *SecretBackup
	Env     *EnvBackup
}

// +kubebuilder:rbac:groups=fluxcd.controlplane.io,resources=resourcesetinputproviders,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=fluxcd.controlplane.io,resources=resourcesetinputproviders/finalizers,verbs=update

// isAppEntry: any app's registry entry, mcloud's or from the homelab repo.
func isAppEntry(o client.Object) bool {
	return o.GetNamespace() == RegistryNamespace && o.GetLabels()[appLabel] == "true"
}

func isMcloudEntry(o client.Object) bool {
	return isAppEntry(o) && o.GetLabels()[ManagedByLabel] == mcloudManaged
}

func backupKey(name string) string { return registryPrefix + name + ".yaml" }

func (r *RegistryBackup) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	if err := r.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !isAppEntry(u) {
		return ctrl.Result{}, nil
	}
	if !u.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(u, RegistryFinalizer) {
			return ctrl.Result{}, nil
		}
		// The app's secrets' and variables' copies go too (whether or not
		// those backups are on), so an app made later under the same name
		// doesn't get them. Every app has those, wherever its entry lives;
		// only mcloud's entries have a copy of the entry itself.
		for _, k := range []string{secretsKey(u.GetName()), envKey(u.GetName()), backupKey(u.GetName())} {
			if err := r.Objects.DeleteObject(ctx, r.Bucket, k); err != nil {
				return ctrl.Result{}, err
			}
		}
		// The registration record (Registrations) stays while the app's
		// buckets do, so the next entry with this name doesn't get them.
		log.FromContext(ctx).Info("app removed from the registry; its copies too", "app", u.GetName())
		controllerutil.RemoveFinalizer(u, RegistryFinalizer)
		return ctrl.Result{}, r.Update(ctx, u)
	}
	// The finalizer, and for an entry mcloud made, its registration ID
	// (Registrations): one from git declares its own there.
	changed := controllerutil.AddFinalizer(u, RegistryFinalizer)
	if isMcloudEntry(u) && u.GetAnnotations()[RegistrationAnnotation] == "" {
		annotations := u.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[RegistrationAnnotation] = NewRegistrationID()
		u.SetAnnotations(annotations)
		changed = true
		log.FromContext(ctx).Info("registry entry given its registration ID", "app", u.GetName(), "registration", annotations[RegistrationAnnotation])
	}
	if changed {
		if err := r.Update(ctx, u); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !isMcloudEntry(u) {
		return ctrl.Result{}, nil // git keeps this one
	}
	data, err := backupOf(u)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cur, found, err := r.Objects.GetObject(ctx, r.Bucket, backupKey(u.GetName())); err != nil {
		return ctrl.Result{}, err
	} else if found && bytes.Equal(cur, data) {
		return ctrl.Result{}, nil
	}
	if err := r.Objects.PutObject(ctx, r.Bucket, backupKey(u.GetName()), data); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("registry entry copied", "app", u.GetName(), "bucket", r.Bucket)
	return ctrl.Result{}, nil
}

// backupOf is what's kept of an entry: enough to create it again, as the
// same registration (its ID), so a rebuilt cluster's app gets its data
// back. The owners are in the spec.
func backupOf(u *unstructured.Unstructured) ([]byte, error) {
	keep := &unstructured.Unstructured{}
	keep.SetGroupVersionKind(providerGVK)
	keep.SetNamespace(u.GetNamespace())
	keep.SetName(u.GetName())
	keep.SetLabels(u.GetLabels())
	if id := u.GetAnnotations()[RegistrationAnnotation]; id != "" {
		keep.SetAnnotations(map[string]string{RegistrationAnnotation: id})
	}
	keep.Object["spec"] = u.Object["spec"]
	return yaml.Marshal(keep.Object)
}

// Restore puts back every entry that has a copy but isn't in the cluster:
// after a rebuild, the registry then recreates the apps (and their
// databases restore themselves from their backups).
func (r *RegistryBackup) Restore(ctx context.Context) error {
	if err := r.Storage.EnsureBucket(ctx, r.Bucket); err != nil {
		return err
	}
	keys, err := r.Objects.Keys(ctx, r.Bucket, registryPrefix, 1000)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if !strings.HasSuffix(k, ".yaml") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(k, registryPrefix), ".yaml")
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(providerGVK)
		err := r.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: name}, cur)
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		data, found, err := r.Objects.GetObject(ctx, r.Bucket, k)
		if err != nil || !found {
			return err
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(data, &u.Object); err != nil {
			return fmt.Errorf("copy %s: %w", k, err)
		}
		if u.GetName() != name || u.GetNamespace() != RegistryNamespace || !isMcloudEntry(u) {
			return fmt.Errorf("copy %s isn't an mcloud registry entry for %s", k, name)
		}
		u.SetGroupVersionKind(providerGVK)
		if err := r.Create(ctx, u); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		log.FromContext(ctx).Info("registry entry restored from its copy", "app", name, "bucket", r.Bucket)
	}
	return nil
}

// Start runs Restore when the controller starts (as the leader), and every
// 15 minutes after, then the apps' secrets' and variables' restores.
func (r *RegistryBackup) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("registry-restore")
	for {
		if err := r.Restore(ctx); err != nil {
			logger.Error(err, "restoring registry entries")
		}
		if r.Secrets != nil {
			if err := r.Secrets.Restore(ctx); err != nil {
				logger.Error(err, "restoring app secrets")
			}
		}
		if r.Env != nil {
			if err := r.Env.Restore(ctx); err != nil {
				logger.Error(err, "restoring app env")
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(15 * time.Minute):
		}
	}
}

func (r *RegistryBackup) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	err := ctrl.NewControllerManagedBy(mgr).
		For(u, builder.WithPredicates(predicate.NewPredicateFuncs(isAppEntry))).
		Named("registry").
		Complete(r)
	if err != nil {
		return err
	}
	return mgr.Add(r) // Start: the restore loop, leader only (the default)
}

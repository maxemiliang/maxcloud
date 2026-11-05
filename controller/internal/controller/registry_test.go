package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const registryBucket = "maxcloud-registry"

func entry(name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	u.SetNamespace(RegistryNamespace)
	u.SetName(name)
	u.SetLabels(labels)
	u.Object["spec"] = map[string]any{"type": "Static", "defaultValues": map[string]any{"name": name, "repo": "o/" + name}}
	return u
}

var mcloudLabels = map[string]string{appLabel: "true", ManagedByLabel: mcloudManaged}

func (e *dbEnv) registry() *RegistryBackup {
	return &RegistryBackup{Client: e.client, Storage: e.storage, Objects: e.objects, Bucket: registryBucket}
}

func (e *dbEnv) reconcileEntry(r *RegistryBackup, name string) {
	e.t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: RegistryNamespace, Name: name}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dbEnv) getEntry(name string) (*unstructured.Unstructured, bool) {
	e.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: RegistryNamespace, Name: name}, u)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return u, true
}

func TestRegistryCopyDeleteAndRestore(t *testing.T) {
	e := newDBEnv(t, entry("shop", mcloudLabels))
	r := e.registry()
	if err := r.Restore(context.Background()); err != nil { // makes the bucket; nothing to restore
		t.Fatal(err)
	}

	// Copied, with a finalizer.
	e.reconcileEntry(r, "shop")
	copyData, ok := e.objects.objects[registryBucket]["apps/shop.yaml"]
	if !ok || !strings.Contains(string(copyData), "repo: o/shop") {
		t.Fatalf("copy = %q", copyData)
	}
	if strings.Contains(string(copyData), "finalizers") || strings.Contains(string(copyData), "resourceVersion") {
		t.Errorf("copy keeps cluster state:\n%s", copyData)
	}
	u, _ := e.getEntry("shop")
	if len(u.GetFinalizers()) != 1 || u.GetFinalizers()[0] != RegistryFinalizer {
		t.Errorf("finalizers = %v", u.GetFinalizers())
	}

	// A rebuilt cluster: the entry is gone without being deleted. Restore
	// puts it back.
	stale := u.DeepCopy()
	stale.SetFinalizers(nil)
	if err := e.client.Update(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := e.client.Delete(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.getEntry("shop"); ok {
		t.Fatal("setup: entry still there")
	}
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	back, ok := e.getEntry("shop")
	if !ok {
		t.Fatal("not restored")
	}
	if v, _, _ := unstructured.NestedString(back.Object, "spec", "defaultValues", "repo"); v != "o/shop" {
		t.Errorf("restored spec = %v", back.Object["spec"])
	}

	// Deleted on purpose: the copy goes, then the entry, and a later
	// Restore doesn't bring it back.
	e.reconcileEntry(r, "shop") // finalizer again
	back, _ = e.getEntry("shop")
	if err := e.client.Delete(context.Background(), back); err != nil {
		t.Fatal(err)
	}
	e.reconcileEntry(r, "shop")
	if _, ok := e.objects.objects[registryBucket]["apps/shop.yaml"]; ok {
		t.Error("copy kept after the app was deleted")
	}
	if _, ok := e.getEntry("shop"); ok {
		t.Error("entry still there (finalizer not removed?)")
	}
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.getEntry("shop"); ok {
		t.Error("a deleted app came back")
	}
}

func TestRegistryIgnoresEntriesInGit(t *testing.T) {
	inGit := entry("run-demo", map[string]string{appLabel: "true", "kustomize.toolkit.fluxcd.io/name": "apps"})
	e := newDBEnv(t, inGit)
	r := e.registry()
	_ = r.Restore(context.Background())
	e.reconcileEntry(r, "run-demo")
	if len(e.objects.objects[registryBucket]) != 0 {
		t.Error("copied an entry that lives in git")
	}
	u, _ := e.getEntry("run-demo")
	if len(u.GetFinalizers()) != 0 {
		t.Error("finalizer on an entry that lives in git")
	}
	var _ client.Object = u
}

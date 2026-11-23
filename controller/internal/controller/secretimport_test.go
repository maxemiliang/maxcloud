package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/maxemiliang/maxcloud/controller/internal/openbao"
)

// fakeKV is OpenBao's KV in memory.
type fakeKV struct {
	secrets map[string]map[string]string
	writes  int
	deletes []string
}

func (f *fakeKV) Exists(_ context.Context, path string) (bool, error) {
	_, ok := f.secrets[path]
	return ok, nil
}

func (f *fakeKV) Write(_ context.Context, path string, data map[string]string, cas int) (int, error) {
	if _, ok := f.secrets[path]; ok && cas == 0 {
		return 0, openbao.ErrCASMismatch
	}
	f.writes++
	f.secrets[path] = data
	return 1, nil
}

func (f *fakeKV) DeleteMetadata(_ context.Context, path string) error {
	f.deletes = append(f.deletes, path)
	delete(f.secrets, path)
	return nil
}

func importOnce(t *testing.T, r *SecretImport, ns string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns}}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretImport(t *testing.T) {
	unlabelled := appSecrets("old", map[string]string{"K": "v"})
	unlabelled.Labels = nil
	e := newDBEnv(t,
		tenantNS("shop"), entry("shop", mcloudLabels), appSecrets("shop", map[string]string{"STRIPE_KEY": "sk", "A": "b"}),
		tenantNS("empty"), entry("empty", mcloudLabels),
		tenantNS("old"), entry("old", mcloudLabels), unlabelled,
		tenantNS("done"), entry("done", mcloudLabels), appSecrets("done", map[string]string{"A": "stale"}),
		tenantNS("leaving"), appSecrets("leaving", map[string]string{"A": "b"}), // no registry entry: being deleted
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}, entry("kube-system", mcloudLabels),
	)
	kv := &fakeKV{secrets: map[string]map[string]string{"done/app-secrets": {"A": "newer"}}}
	r := &SecretImport{Client: e.client, APIReader: e.client, KV: kv}
	for _, ns := range []string{"shop", "empty", "old", "done", "leaving", "kube-system"} {
		importOnce(t, r, ns)
	}
	if got := kv.secrets["shop/app-secrets"]; got["STRIPE_KEY"] != "sk" || got["A"] != "b" || len(got) != 2 {
		t.Errorf("shop: %v", got)
	}
	// No Secret: an empty KV secret, so ESO syncs it without an error.
	if got, ok := kv.secrets["empty/app-secrets"]; !ok || len(got) != 0 {
		t.Errorf("empty: %v %v", got, ok)
	}
	if kv.secrets["old/app-secrets"]["K"] != "v" {
		t.Errorf("a Secret without mcloud's label wasn't imported: %v", kv.secrets["old/app-secrets"])
	}
	// OpenBao's is never overwritten.
	if kv.secrets["done/app-secrets"]["A"] != "newer" {
		t.Errorf("overwrote: %v", kv.secrets["done/app-secrets"])
	}
	for _, ns := range []string{"leaving", "kube-system"} {
		if _, ok := kv.secrets[ns+"/app-secrets"]; ok {
			t.Errorf("%s: imported", ns)
		}
	}
	// Again: nothing to do.
	n := kv.writes
	importOnce(t, r, "shop")
	if kv.writes != n {
		t.Error("imported twice")
	}
}

// With OpenBao, an app's deletion deletes its KV secret too, and the
// Secret still in its namespace isn't imported again.
func TestRegistryDeleteRemovesKVSecret(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appSecrets("shop", map[string]string{"A": "b"}))
	if err := e.storage.EnsureBucket(context.Background(), registryBucket); err != nil {
		t.Fatal(err)
	}
	kv := &fakeKV{secrets: map[string]map[string]string{}}
	reg := e.registry()
	reg.OpenBao = kv
	e.reconcileEntry(reg, "shop")
	imp := &SecretImport{Client: e.client, APIReader: e.client, KV: kv}
	importOnce(t, imp, "shop")
	if _, ok := kv.secrets["shop/app-secrets"]; !ok {
		t.Fatal("setup: not imported")
	}
	u, _ := e.getEntry("shop")
	e.client.Delete(context.Background(), u)
	e.reconcileEntry(reg, "shop")
	if len(kv.deletes) != 1 || kv.deletes[0] != "shop/app-secrets" {
		t.Errorf("deletes %v", kv.deletes)
	}
	importOnce(t, imp, "shop")
	if _, ok := kv.secrets["shop/app-secrets"]; ok {
		t.Error("imported again after the app was deleted")
	}
}

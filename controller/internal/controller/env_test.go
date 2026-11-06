package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func appEnv(ns string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: AppEnvName,
		Labels:      map[string]string{ManagedByLabel: mcloudManaged},
		Annotations: map[string]string{pruneAnnotation: "disabled"},
	}, Data: data}
}

type envEnv struct {
	*dbEnv
	r *EnvBackup
}

func newEnvEnv(t *testing.T, objs ...client.Object) *envEnv {
	t.Helper()
	e := newDBEnv(t, objs...)
	if err := e.storage.EnsureBucket(context.Background(), registryBucket); err != nil {
		t.Fatal(err)
	}
	return &envEnv{dbEnv: e, r: &EnvBackup{Client: e.client, Storage: e.storage, Objects: e.objects, Bucket: registryBucket}}
}

func (e *envEnv) reconcile(ns string) {
	e.t.Helper()
	if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: AppEnvName}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *envEnv) stored(app string) (string, bool) {
	d, ok := e.objects.objects[registryBucket][envKey(app)]
	return string(d), ok
}

func (e *envEnv) configMap(ns string) (*corev1.ConfigMap, bool) {
	e.t.Helper()
	var cm corev1.ConfigMap
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: AppEnvName}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return &cm, true
}

func TestEnvBackupCopies(t *testing.T) {
	e := newEnvEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appEnv("shop", map[string]string{"LOG_LEVEL": "debug", "A": "b"}))
	e.reconcile("shop")
	if got, _ := e.stored("shop"); got != `{"A":"b","LOG_LEVEL":"debug"}` {
		t.Errorf("copy = %s", got)
	}
	cm, _ := e.configMap("shop")
	cm.Data = nil
	e.client.Update(context.Background(), cm)
	e.reconcile("shop")
	if got, _ := e.stored("shop"); got != `{}` {
		t.Errorf("copy of an empty ConfigMap = %s", got)
	}
}

func TestEnvBackupSkips(t *testing.T) {
	notMcloud := appEnv("shop", map[string]string{"A": "b"})
	notMcloud.Labels = nil
	gone := appEnv("gone", map[string]string{"A": "b"})
	e := newEnvEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), notMcloud,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}, entry("plain", mcloudLabels), appEnv("plain", map[string]string{"A": "b"}),
		tenantNS("gone"), gone) // no registry entry: the app is being deleted
	for _, ns := range []string{"shop", "plain", "gone"} {
		e.reconcile(ns)
		if _, ok := e.stored(ns); ok {
			t.Errorf("%s: copied", ns)
		}
	}
}

func TestEnvBackupRestores(t *testing.T) {
	e := newEnvEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appEnv("shop", map[string]string{"A": "b"}))
	e.reconcile("shop")

	// Deleted by hand: the reconcile that follows puts it back.
	cm, _ := e.configMap("shop")
	e.client.Delete(context.Background(), cm)
	e.reconcile("shop")
	back, ok := e.configMap("shop")
	if !ok {
		t.Fatal("not restored")
	}
	if back.Data["A"] != "b" || back.Labels[ManagedByLabel] != "mcloud" {
		t.Errorf("restored = %v %v", back.Data, back.Labels)
	}

	// A rebuilt cluster: the copy is there, the namespace not yet.
	e.client.Delete(context.Background(), back)
	ns := &corev1.Namespace{}
	e.client.Get(context.Background(), types.NamespacedName{Name: "shop"}, ns)
	e.client.Delete(context.Background(), ns)
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.configMap("shop"); ok {
		t.Fatal("restored into a missing namespace")
	}
	e.client.Create(context.Background(), tenantNS("shop"))
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cm, ok := e.configMap("shop"); !ok || cm.Data["A"] != "b" {
		t.Fatalf("not restored: %v", cm)
	}
}

func TestRegistryDeleteRemovesEnvCopy(t *testing.T) {
	e := newEnvEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appEnv("shop", map[string]string{"A": "b"}))
	reg := e.registry()
	reg.Env = e.r
	e.reconcileEntry(reg, "shop")
	e.reconcile("shop")
	if _, ok := e.stored("shop"); !ok {
		t.Fatal("setup: no copy")
	}
	u, _ := e.getEntry("shop")
	e.client.Delete(context.Background(), u)
	e.reconcileEntry(reg, "shop")
	if _, ok := e.stored("shop"); ok {
		t.Error("env copy kept after the app was deleted")
	}
	e.reconcile("shop")
	if _, ok := e.stored("shop"); ok {
		t.Error("copied again after the app was deleted")
	}
}

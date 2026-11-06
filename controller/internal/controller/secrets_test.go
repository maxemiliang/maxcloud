package controller

import (
	"bytes"
	"context"
	"testing"

	"filippo.io/age"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func appSecrets(ns string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: AppSecretsName,
		Labels:      map[string]string{ManagedByLabel: mcloudManaged},
		Annotations: map[string]string{pruneAnnotation: "disabled"},
	}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

type secretsEnv struct {
	*dbEnv
	r  *SecretBackup
	id *age.X25519Identity
}

func newSecretsEnv(t *testing.T, objs ...client.Object) *secretsEnv {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	e := newDBEnv(t, objs...)
	if err := e.storage.EnsureBucket(context.Background(), registryBucket); err != nil {
		t.Fatal(err)
	}
	return &secretsEnv{dbEnv: e, id: id, r: &SecretBackup{
		Client: e.client, Storage: e.storage, Objects: e.objects, Bucket: registryBucket, Identity: id,
	}}
}

func (e *secretsEnv) reconcile(ns string) {
	e.t.Helper()
	if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: AppSecretsName}}); err != nil {
		e.t.Fatal(err)
	}
}

// stored decrypts the copy of the app's secrets.
func (e *secretsEnv) stored(app string) (string, bool) {
	e.t.Helper()
	enc, ok := e.objects.objects[registryBucket][secretsKey(app)]
	if !ok {
		return "", false
	}
	data, err := e.r.decrypt(enc)
	if err != nil {
		e.t.Fatal(err)
	}
	j, _ := secretJSON(data)
	return string(j), true
}

func (e *secretsEnv) secret(ns string) (*corev1.Secret, bool) {
	e.t.Helper()
	var s corev1.Secret
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: AppSecretsName}, &s)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return &s, true
}

func TestSecretBackupCopiesEncrypted(t *testing.T) {
	e := newSecretsEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appSecrets("shop", map[string]string{"STRIPE_KEY": "sk_live_123", "A": "b"}))
	e.reconcile("shop")
	enc := e.objects.objects[registryBucket]["secrets/shop.age"]
	if bytes.Contains(enc, []byte("sk_live_123")) || bytes.Contains(enc, []byte("STRIPE_KEY")) {
		t.Fatal("copy isn't encrypted")
	}
	if got, _ := e.stored("shop"); got != `{"A":"b","STRIPE_KEY":"sk_live_123"}` {
		t.Errorf("copy = %s", got)
	}

	// Unchanged: not written again (age output differs on every write).
	e.reconcile("shop")
	if !bytes.Equal(e.objects.objects[registryBucket]["secrets/shop.age"], enc) {
		t.Error("rewrote an unchanged copy")
	}

	// Changed, then emptied: an empty Secret is kept as {}.
	s, _ := e.secret("shop")
	s.Data = map[string][]byte{"A": []byte("c")}
	e.client.Update(context.Background(), s)
	e.reconcile("shop")
	if got, _ := e.stored("shop"); got != `{"A":"c"}` {
		t.Errorf("copy = %s", got)
	}
	s, _ = e.secret("shop")
	s.Data = nil
	e.client.Update(context.Background(), s)
	e.reconcile("shop")
	if got, _ := e.stored("shop"); got != `{}` {
		t.Errorf("copy of an empty Secret = %s", got)
	}

	// No hash or anything else put on the Secret.
	s, _ = e.secret("shop")
	if len(s.Annotations) != 1 || len(s.Labels) != 1 {
		t.Errorf("Secret metadata changed: %v %v", s.Labels, s.Annotations)
	}
}

func TestSecretBackupSkips(t *testing.T) {
	notMcloud := appSecrets("shop", map[string]string{"A": "b"})
	notMcloud.Labels = nil
	other := appSecrets("plain", map[string]string{"A": "b"})
	gone := appSecrets("gone", map[string]string{"A": "b"})
	e := newSecretsEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), notMcloud,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}, entry("plain", mcloudLabels), other,
		tenantNS("gone"), gone) // no registry entry: the app is being deleted
	for _, ns := range []string{"shop", "plain", "gone"} {
		e.reconcile(ns)
		if _, ok := e.stored(ns); ok {
			t.Errorf("%s: copied", ns)
		}
	}
}

func TestSecretBackupRestores(t *testing.T) {
	e := newSecretsEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appSecrets("shop", map[string]string{"A": "b"}))
	e.reconcile("shop")

	// Deleted by hand: the reconcile that follows puts it back.
	s, _ := e.secret("shop")
	e.client.Delete(context.Background(), s)
	e.reconcile("shop")
	back, ok := e.secret("shop")
	if !ok {
		t.Fatal("not restored")
	}
	if string(back.Data["A"]) != "b" || back.Labels[ManagedByLabel] != "mcloud" || back.Annotations[pruneAnnotation] != "disabled" {
		t.Errorf("restored = %v %v %v", back.Data, back.Labels, back.Annotations)
	}

	// A rebuilt cluster: the copy is there, the namespace not yet.
	e.client.Delete(context.Background(), back)
	ns := &corev1.Namespace{}
	e.client.Get(context.Background(), types.NamespacedName{Name: "shop"}, ns)
	e.client.Delete(context.Background(), ns)
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.secret("shop"); ok {
		t.Fatal("restored into a missing namespace")
	}
	// The namespace appears: the loop's next Restore brings the Secret back.
	e.client.Create(context.Background(), tenantNS("shop"))
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, ok := e.secret("shop"); !ok || string(s.Data["A"]) != "b" {
		t.Fatalf("not restored: %v", s)
	}
	// And again: nothing to do.
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSecretBackupRestoreSkipsNonTenants(t *testing.T) {
	e := newSecretsEnv(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})
	enc, _ := e.r.encrypt([]byte(`{"A":"b"}`))
	e.objects.objects[registryBucket] = map[string][]byte{"secrets/kube-system.age": enc}
	if err := e.r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.secret("kube-system"); ok {
		t.Error("restored into a namespace that isn't an app's")
	}
}

func TestSecretBackupWrongKey(t *testing.T) {
	e := newSecretsEnv(t, tenantNS("shop"))
	other, _ := age.GenerateX25519Identity()
	foreign := &SecretBackup{Identity: other}
	enc, _ := foreign.encrypt([]byte(`{"A":"b"}`))
	e.objects.objects[registryBucket] = map[string][]byte{"secrets/shop.age": enc}
	if err := e.r.Restore(context.Background()); err == nil {
		t.Error("restored a copy it can't decrypt")
	}
	if _, ok := e.secret("shop"); ok {
		t.Error("Secret created")
	}
}

func TestRegistryDeleteRemovesSecretsCopy(t *testing.T) {
	e := newSecretsEnv(t, tenantNS("shop"), entry("shop", mcloudLabels), appSecrets("shop", map[string]string{"A": "b"}))
	reg := e.registry()
	reg.Secrets = e.r
	e.reconcileEntry(reg, "shop")
	e.reconcile("shop")
	if _, ok := e.stored("shop"); !ok {
		t.Fatal("setup: no copy")
	}
	u, _ := e.getEntry("shop")
	e.client.Delete(context.Background(), u)
	e.reconcileEntry(reg, "shop")
	if _, ok := e.objects.objects[registryBucket]["secrets/shop.age"]; ok {
		t.Error("secrets' copy kept after the app was deleted")
	}
	// Its namespace isn't gone yet; the Secret changing doesn't copy it again.
	e.reconcile("shop")
	if _, ok := e.stored("shop"); ok {
		t.Error("copied again after the app was deleted")
	}
}

func TestParseAgeIdentity(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	got, err := ParseAgeIdentity("  " + id.String() + "\n")
	if err != nil || got.Recipient().String() != id.Recipient().String() {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := ParseAgeIdentity("nope"); err == nil {
		t.Error("accepted a bad key")
	}
}

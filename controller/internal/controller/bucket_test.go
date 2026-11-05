package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// fakeStorage is UpCloud Object Storage in memory.
type fakeStorage struct {
	buckets  map[string]objectstorage.BucketMetrics
	users    map[string][]string // username -> access key IDs
	policies map[string]string   // name -> document
	attached map[string][]string // username -> policy names
	nextKey  int
	calls    []string
	fail     string // a method name that returns an error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		buckets:  map[string]objectstorage.BucketMetrics{},
		users:    map[string][]string{},
		policies: map[string]string{},
		attached: map[string][]string{},
	}
}

func (f *fakeStorage) call(name string, args ...string) error {
	f.calls = append(f.calls, name+"("+strings.Join(args, ",")+")")
	if f.fail == name {
		return fmt.Errorf("%s: UpCloud says no", name)
	}
	return nil
}

func (f *fakeStorage) Buckets(context.Context) (map[string]objectstorage.BucketMetrics, error) {
	if err := f.call("Buckets"); err != nil {
		return nil, err
	}
	return f.buckets, nil
}
func (f *fakeStorage) EnsureBucket(_ context.Context, name string) error {
	if err := f.call("EnsureBucket", name); err != nil {
		return err
	}
	if _, ok := f.buckets[name]; !ok {
		f.buckets[name] = objectstorage.BucketMetrics{}
	}
	return nil
}
func (f *fakeStorage) DeleteBucket(_ context.Context, name string) error {
	if err := f.call("DeleteBucket", name); err != nil {
		return err
	}
	delete(f.buckets, name)
	return nil
}
func (f *fakeStorage) EnsureUser(_ context.Context, u string) error {
	if err := f.call("EnsureUser", u); err != nil {
		return err
	}
	if _, ok := f.users[u]; !ok {
		f.users[u] = nil
	}
	return nil
}
func (f *fakeStorage) DeleteUser(_ context.Context, u string) error {
	if err := f.call("DeleteUser", u); err != nil {
		return err
	}
	delete(f.users, u)
	delete(f.attached, u)
	return nil
}
func (f *fakeStorage) EnsurePolicy(_ context.Context, name, _, doc string) error {
	if err := f.call("EnsurePolicy", name); err != nil {
		return err
	}
	f.policies[name] = doc
	return nil
}
func (f *fakeStorage) DeletePolicy(_ context.Context, name string) error {
	if err := f.call("DeletePolicy", name); err != nil {
		return err
	}
	delete(f.policies, name)
	return nil
}
func (f *fakeStorage) EnsureUserPolicy(_ context.Context, u, p string) error {
	if err := f.call("EnsureUserPolicy", u, p); err != nil {
		return err
	}
	if !slices.Contains(f.attached[u], p) {
		f.attached[u] = append(f.attached[u], p)
	}
	return nil
}
func (f *fakeStorage) AccessKeys(_ context.Context, u string) ([]string, error) {
	if err := f.call("AccessKeys", u); err != nil {
		return nil, err
	}
	return slices.Clone(f.users[u]), nil
}
func (f *fakeStorage) CreateAccessKey(_ context.Context, u string) (string, string, error) {
	if err := f.call("CreateAccessKey", u); err != nil {
		return "", "", err
	}
	f.nextKey++
	id := fmt.Sprintf("KEY%d", f.nextKey)
	f.users[u] = append(f.users[u], id)
	return id, "secret-" + id, nil
}
func (f *fakeStorage) DeleteAccessKey(_ context.Context, u, id string) error {
	if err := f.call("DeleteAccessKey", u, id); err != nil {
		return err
	}
	f.users[u] = slices.DeleteFunc(f.users[u], func(k string) bool { return k == id })
	return nil
}

type fakePolicies struct{ policies map[string]string }

func (f *fakePolicies) PutBucketPolicy(_ context.Context, bucket, doc string) error {
	f.policies[bucket] = doc
	return nil
}
func (f *fakePolicies) DeleteBucketPolicy(_ context.Context, bucket string) error {
	delete(f.policies, bucket)
	return nil
}

type env struct {
	t        *testing.T
	client   client.Client
	storage  *fakeStorage
	policies *fakePolicies
	identity *Identity
	r        *BucketReconciler
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1.Bucket{}, &v1.Database{}).Build()
	s := newFakeStorage()
	p := &fakePolicies{policies: map[string]string{}}
	id := &Identity{Client: c, APIReader: c, Storage: s, Endpoint: "https://example.upcloudobjects.com", Region: "europe-1"}
	return &env{t: t, client: c, storage: s, policies: p, identity: id, r: &BucketReconciler{
		Client:   c,
		Identity: id,
		Storage:  s,
		Policies: p,
		Metrics:  &MetricsCache{Storage: s, MaxAge: 0},
		Endpoint: "https://example.upcloudobjects.com",
	}}
}

func tenantNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{TenantLabel: "true"}}}
}

func bucket(ns, name string, mod ...func(*v1.Bucket)) *v1.Bucket {
	b := &v1.Bucket{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1}, Spec: v1.BucketSpec{ReclaimPolicy: v1.Retain}}
	for _, m := range mod {
		m(b)
	}
	return b
}

func (e *env) reconcile(ns, name string) (ctrl.Result, error) {
	e.t.Helper()
	return e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
}

func (e *env) mustReconcile(ns, name string) ctrl.Result {
	e.t.Helper()
	res, err := e.reconcile(ns, name)
	if err != nil {
		e.t.Fatalf("reconcile %s/%s: %v", ns, name, err)
	}
	return res
}

func (e *env) bucket(ns, name string) *v1.Bucket {
	e.t.Helper()
	var b v1.Bucket
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		e.t.Fatal(err)
	}
	return &b
}

func (e *env) secret(ns string) (*corev1.Secret, bool) {
	e.t.Helper()
	var s corev1.Secret
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "maxcloud-s3"}, &s)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return &s, true
}

func (e *env) delete(ns, name string) {
	e.t.Helper()
	if err := e.client.Delete(context.Background(), e.bucket(ns, name)); err != nil {
		e.t.Fatal(err)
	}
}

func ready(b *v1.Bucket) *metav1.Condition {
	return meta.FindStatusCondition(b.Status.Conditions, "Ready")
}

func TestNewBucket(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"))
	res := e.mustReconcile("shop", "uploads")
	if res.RequeueAfter != resync {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, resync)
	}

	if _, ok := e.storage.buckets["mc-shop--uploads"]; !ok {
		t.Error("bucket mc-shop--uploads wasn't created")
	}
	if !slices.Equal(e.storage.attached["mc-shop"], []string{"mc-shop"}) {
		t.Errorf("user mc-shop has policies %v, want [mc-shop]", e.storage.attached["mc-shop"])
	}
	if !strings.Contains(e.storage.policies["mc-shop"], "mc-shop--") {
		t.Errorf("policy mc-shop doesn't cover mc-shop--*: %s", e.storage.policies["mc-shop"])
	}

	s, ok := e.secret("shop")
	if !ok {
		t.Fatal("no Secret maxcloud-s3")
	}
	for k, want := range map[string]string{
		"AWS_ACCESS_KEY_ID":                "KEY1",
		"AWS_SECRET_ACCESS_KEY":            "secret-KEY1",
		"AWS_ENDPOINT_URL_S3":              "https://example.upcloudobjects.com",
		"AWS_REGION":                       "europe-1",
		"AWS_REQUEST_CHECKSUM_CALCULATION": "when_required",
		"MAXCLOUD_APP":                     "shop",
	} {
		if got := string(s.Data[k]); got != want {
			t.Errorf("Secret %s = %q, want %q", k, got, want)
		}
	}
	if s.Labels[ManagedByLabel] != managedBy {
		t.Errorf("Secret labels = %v", s.Labels)
	}

	b := e.bucket("shop", "uploads")
	if c := ready(b); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v, want True", c)
	}
	if b.Status.BucketName != "mc-shop--uploads" || b.Status.Endpoint == "" {
		t.Errorf("status = %+v", b.Status)
	}
	if !slices.Contains(b.Finalizers, BucketFinalizer) {
		t.Error("no finalizer")
	}
	if _, public := e.policies.policies["mc-shop--uploads"]; public {
		t.Error("private bucket got a bucket policy")
	}
}

func TestSecondBucketReusesTheKey(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"), bucket("shop", "assets"))
	e.mustReconcile("shop", "uploads")
	e.mustReconcile("shop", "assets")
	e.mustReconcile("shop", "uploads")
	if e.storage.nextKey != 1 {
		t.Errorf("%d access keys created, want 1", e.storage.nextKey)
	}
	if len(e.storage.buckets) != 2 {
		t.Errorf("buckets = %v", e.storage.buckets)
	}
}

func TestLostSecretGetsANewKey(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"))
	e.mustReconcile("shop", "uploads")
	s, _ := e.secret("shop")
	if err := e.client.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile("shop", "uploads")

	s, ok := e.secret("shop")
	if !ok || string(s.Data["AWS_ACCESS_KEY_ID"]) != "KEY2" {
		t.Fatalf("Secret after recreation: %v", s)
	}
	// The old key's secret is lost, so it's deleted.
	if !slices.Equal(e.storage.users["mc-shop"], []string{"KEY2"}) {
		t.Errorf("keys = %v, want [KEY2]", e.storage.users["mc-shop"])
	}
}

func TestPublicBucket(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "assets", func(b *v1.Bucket) { b.Spec.Public = true }))
	e.mustReconcile("shop", "assets")
	doc := e.policies.policies["mc-shop--assets"]
	if !strings.Contains(doc, `"Principal":"*"`) || !strings.Contains(doc, "mc-shop--assets/*") {
		t.Fatalf("bucket policy = %q", doc)
	}

	// Made private again: a new generation, and the policy goes.
	b := e.bucket("shop", "assets")
	b.Spec.Public = false
	b.Generation = 2
	if err := e.client.Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile("shop", "assets")
	if _, ok := e.policies.policies["mc-shop--assets"]; ok {
		t.Error("bucket policy still there after making the bucket private")
	}
}

func TestNotATenant(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}
	e := newEnv(t, ns, bucket("kube-system", "x"))
	e.mustReconcile("kube-system", "x")
	if len(e.storage.calls) != 0 {
		t.Errorf("UpCloud was called: %v", e.storage.calls)
	}
	if c := ready(e.bucket("kube-system", "x")); c == nil || c.Reason != ReasonNotTenant {
		t.Errorf("Ready = %+v, want reason %s", c, ReasonNotTenant)
	}
}

func TestInvalidName(t *testing.T) {
	// The CRD rejects these; the controller doesn't rely on it.
	e := newEnv(t, tenantNS("shop"), bucket("shop", "a--b"))
	e.mustReconcile("shop", "a--b")
	if len(e.storage.calls) != 0 {
		t.Errorf("UpCloud was called: %v", e.storage.calls)
	}
	if c := ready(e.bucket("shop", "a--b")); c == nil || c.Reason != ReasonBadName {
		t.Errorf("Ready = %+v, want reason %s", c, ReasonBadName)
	}
}

func TestAPIErrorIsReported(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"))
	e.storage.fail = "EnsureBucket"
	if _, err := e.reconcile("shop", "uploads"); err == nil {
		t.Fatal("reconcile should fail")
	}
	c := ready(e.bucket("shop", "uploads"))
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != ReasonAPIError || !strings.Contains(c.Message, "UpCloud says no") {
		t.Errorf("Ready = %+v", c)
	}
}

func TestDeleteRetainKeepsTheBucket(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"), bucket("shop", "assets"))
	e.mustReconcile("shop", "uploads")
	e.mustReconcile("shop", "assets")

	// Not the last bucket: the credentials stay.
	e.delete("shop", "uploads")
	e.mustReconcile("shop", "uploads")
	if _, ok := e.storage.buckets["mc-shop--uploads"]; !ok {
		t.Error("Retain: the bucket was deleted")
	}
	if _, ok := e.secret("shop"); !ok {
		t.Error("the Secret went while another bucket still needs it")
	}
	if _, ok := e.storage.users["mc-shop"]; !ok {
		t.Error("the user went while another bucket still needs it")
	}
	var b v1.Bucket
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: "uploads"}, &b)
	if !apierrors.IsNotFound(err) {
		t.Errorf("Bucket uploads still exists (finalizer not removed?): %v", err)
	}

	// The last one: the credentials go, the bucket stays.
	e.delete("shop", "assets")
	e.mustReconcile("shop", "assets")
	if _, ok := e.storage.buckets["mc-shop--assets"]; !ok {
		t.Error("Retain: the bucket was deleted")
	}
	if _, ok := e.secret("shop"); ok {
		t.Error("the Secret is still there after the last bucket went")
	}
	if _, ok := e.storage.users["mc-shop"]; ok {
		t.Error("the user is still there after the last bucket went")
	}
	if _, ok := e.storage.policies["mc-shop"]; ok {
		t.Error("the policy is still there after the last bucket went")
	}
}

func TestDeleteDeletesTheBucket(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "tmp", func(b *v1.Bucket) { b.Spec.ReclaimPolicy = v1.Delete }))
	e.mustReconcile("shop", "tmp")
	e.delete("shop", "tmp")
	e.mustReconcile("shop", "tmp")
	if _, ok := e.storage.buckets["mc-shop--tmp"]; ok {
		t.Error("Delete: the bucket is still there")
	}
}

func TestMetricsInStatus(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "uploads"))
	e.storage.buckets["mc-shop--uploads"] = objectstorage.BucketMetrics{SizeBytes: 2048, Objects: 3}
	e.r.Metrics.MaxAge = time.Hour
	e.mustReconcile("shop", "uploads")
	b := e.bucket("shop", "uploads")
	if b.Status.SizeBytes == nil || *b.Status.SizeBytes != 2048 || b.Status.Objects == nil || *b.Status.Objects != 3 {
		t.Errorf("status = %+v", b.Status)
	}
}

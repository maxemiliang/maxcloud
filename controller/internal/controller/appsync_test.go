package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

type syncEnv struct {
	t       *testing.T
	c       client.Client
	r       *AppSync
	served  []byte
	digest  string
	writeOK bool
}

func newSyncEnv(t *testing.T, entryLabels map[string]string, files map[string]string) *syncEnv {
	e := &syncEnv{t: t, writeOK: true}
	e.served = tarball(t, files)
	sum := sha256.Sum256(e.served)
	e.digest = "sha256:" + hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(e.served) }))
	t.Cleanup(srv.Close)

	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(gitRepositoryGVK)
	repo.SetNamespace(RegistryNamespace)
	repo.SetName("shop")
	repo.SetLabels(map[string]string{"app.kubernetes.io/part-of": "maxcloud"})
	repo.Object["status"] = map[string]any{"artifact": map[string]any{
		"revision": "main@sha1:abc", "url": srv.URL + "/a.tar.gz", "digest": e.digest,
	}}
	ent := entry("shop", entryLabels)
	ent.Object["spec"] = map[string]any{"type": "Static", "defaultValues": map[string]any{"name": "shop", "repo": "o/shop", "cpu": "200m"}}

	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	e.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(repo, ent).Build()
	writer := interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if !e.writeOK {
				return apierrors.NewForbidden(schema.GroupResource{Group: "fluxcd.controlplane.io", Resource: "resourcesetinputproviders"},
					"shop", errorString("ValidatingAdmissionPolicy 'maxcloud-registry-limits' denied request: quota: cpu at most 1"))
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	e.r = &AppSync{Client: e.c, Writer: writer}
	return e
}

type errorString string

func (s errorString) Error() string { return string(s) }

func (e *syncEnv) reconcile() error {
	_, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: RegistryNamespace, Name: "shop"}})
	return err
}

func (e *syncEnv) entry() *unstructured.Unstructured {
	e.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: RegistryNamespace, Name: "shop"}, u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func values(u *unstructured.Unstructured) map[string]any {
	v, _, _ := unstructured.NestedMap(u.Object, "spec", "defaultValues")
	return v
}

func TestAppSyncAppliesMaxcloudYAML(t *testing.T) {
	e := newSyncEnv(t, mcloudLabels, map[string]string{
		"maxcloud.yaml":     "app: shop\nquota: {cpu: 500m, pods: 30}\nbuckets: [uploads, assets:public]\ndb: {size: 2Gi}\nrepo: evil/other\n",
		"deploy/k8s/x.yaml": "...",
	})
	if err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	v := values(e.entry())
	for k, want := range map[string]string{"cpu": "500m", "pods": "30", "buckets": "uploads,assets:public", "db": "true", "dbSize": "2Gi", "repo": "o/shop", "name": "shop"} {
		if v[k] != want {
			t.Errorf("%s = %v, want %s", k, v[k], want)
		}
	}
	a := e.entry().GetAnnotations()
	if a[SyncedRevisionAnnotation] != "main@sha1:abc" || a[SyncErrorAnnotation] != "" {
		t.Errorf("annotations = %v", a)
	}
	// The same revision again: nothing to do.
	before := e.entry().GetResourceVersion()
	if err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	if e.entry().GetResourceVersion() != before {
		t.Error("synced the same revision twice")
	}
}

func TestAppSyncRefusedByTheCeiling(t *testing.T) {
	e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "app: shop\nquota: {cpu: '8'}\n"})
	e.writeOK = false
	if err := e.reconcile(); err != nil {
		t.Fatalf("a refusal is reported, not retried: %v", err)
	}
	ent := e.entry()
	if values(ent)["cpu"] != "200m" {
		t.Errorf("cpu = %v", values(ent)["cpu"])
	}
	if a := ent.GetAnnotations(); !strings.Contains(a[SyncErrorAnnotation], "cpu at most 1") || a[SyncedRevisionAnnotation] != "main@sha1:abc" {
		t.Errorf("annotations = %v", a)
	}
}

func TestAppSyncOtherCases(t *testing.T) {
	t.Run("maxcloud.yaml for another app", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "app: other\nquota: {cpu: 500m}\n"})
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if a := e.entry().GetAnnotations(); !strings.Contains(a[SyncErrorAnnotation], `not "shop"`) || values(e.entry())["cpu"] != "200m" {
			t.Errorf("annotations = %v, cpu %v", a, values(e.entry())["cpu"])
		}
	})
	t.Run("no maxcloud.yaml", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"deploy/k8s/x.yaml": "..."})
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if a := e.entry().GetAnnotations(); a[SyncedRevisionAnnotation] != "main@sha1:abc" || values(e.entry())["cpu"] != "200m" {
			t.Errorf("annotations = %v", a)
		}
	})
	t.Run("tampered artifact", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "app: shop\nquota: {cpu: 500m}\n"})
		e.served = tarball(t, map[string]string{"maxcloud.yaml": "app: shop\nquota: {cpu: 900m}\n"})
		if err := e.reconcile(); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Errorf("want a digest error, got %v", err)
		}
		if values(e.entry())["cpu"] != "200m" {
			t.Error("applied a tampered artifact")
		}
	})
	t.Run("entry in git", func(t *testing.T) {
		e := newSyncEnv(t, map[string]string{appLabel: "true", "kustomize.toolkit.fluxcd.io/name": "apps"},
			map[string]string{"maxcloud.yaml": "app: shop\nquota: {cpu: 500m}\n"})
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if values(e.entry())["cpu"] != "200m" || e.entry().GetAnnotations()[SyncedRevisionAnnotation] != "" {
			t.Error("synced an entry that lives in git")
		}
	})
	t.Run("db: false removes the database", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "app: shop\ndb: false\n"})
		ent := e.entry()
		v := values(ent)
		v["db"], v["dbSize"] = "true", "5Gi"
		unstructured.SetNestedMap(ent.Object, v, "spec", "defaultValues")
		e.c.Update(context.Background(), ent)
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if v := values(e.entry()); v["db"] != nil || v["dbSize"] != nil {
			t.Errorf("values = %v", v)
		}
	})
}

func TestAppSyncDomains(t *testing.T) {
	e := newSyncEnv(t, mcloudLabels, map[string]string{
		"maxcloud.yaml": "app: shop\ndomains:\n  - {host: Shop.example.com, service: web, proxied: true}\n  - {host: api.example.com, service: api}\n",
	})
	if err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := values(e.entry())["domains"]; got != "shop.example.com:web:proxied,api.example.com:api" {
		t.Errorf("domains = %v", got)
	}
}

func TestAppSyncRecordsSyncedFields(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"app: shop\nquota: {cpu: 500m, pods: 30}\nbuckets: [uploads]\ndb: {size: 2Gi}\n", "buckets,cpu,db,dbInstances,dbSize,pods"},
		{"quota: {cpu: '1'}\n", "cpu"},
		{"quota: {memory: 1Gi}\ndb: true\n", "db,dbInstances,dbSize,memory"},
		{"db: false\n", "db,dbInstances,dbSize"},
		{"buckets: []\n", "buckets"},
		{"domains: [{host: shop.example.com, service: web}]\n", "domains"},
		{"deploy: gitops\nquota: {cpu: '1'}\n", "cpu,deploy"},
		{"app: shop\n", ""},
	} {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": tc.file})
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		a := e.entry().GetAnnotations()
		if got, ok := a[SyncedFieldsAnnotation]; !ok || got != tc.want {
			t.Errorf("%q: synced-fields = %q (set: %v), want %q", tc.file, got, ok, tc.want)
		}
	}

	t.Run("set even when nothing changes", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "quota: {cpu: 200m}\n"})
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if a := e.entry().GetAnnotations(); a[SyncedFieldsAnnotation] != "cpu" {
			t.Errorf("annotations = %v", a)
		}
	})
	t.Run("removed when the revision has no maxcloud.yaml", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"deploy/k8s/x.yaml": "..."})
		ent := e.entry()
		ent.SetAnnotations(map[string]string{SyncedFieldsAnnotation: "cpu"})
		e.c.Update(context.Background(), ent)
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if a := e.entry().GetAnnotations(); a[SyncedRevisionAnnotation] != "main@sha1:abc" {
			t.Errorf("annotations = %v", a)
		} else if _, ok := a[SyncedFieldsAnnotation]; ok {
			t.Errorf("synced-fields kept: %v", a)
		}
	})
	t.Run("a failed sync leaves the previous value", func(t *testing.T) {
		e := newSyncEnv(t, mcloudLabels, map[string]string{"maxcloud.yaml": "app: shop\nquota: {cpu: '8'}\nbuckets: [x]\n"})
		ent := e.entry()
		ent.SetAnnotations(map[string]string{SyncedFieldsAnnotation: "memory"})
		e.c.Update(context.Background(), ent)
		e.writeOK = false
		if err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
		if a := e.entry().GetAnnotations(); a[SyncedFieldsAnnotation] != "memory" || a[SyncErrorAnnotation] == "" {
			t.Errorf("annotations = %v", a)
		}
	})
}

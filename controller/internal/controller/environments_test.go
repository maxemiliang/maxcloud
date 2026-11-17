package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

var envNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

type envsEnv struct {
	t       *testing.T
	c       client.Client
	r       *Environments
	objects *fakeObjects
}

func newEnvsEnv(t *testing.T, objs ...client.Object) *envsEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	v1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1.Preview{}).Build()
	o := &fakeObjects{storage: newFakeStorage(), objects: map[string]map[string][]byte{}}
	o.storage.buckets[registryBucket] = objectstorage.BucketMetrics{}
	return &envsEnv{t: t, c: c, objects: o, r: &Environments{
		Client: c, Writer: c, Objects: o, Bucket: registryBucket,
		Now: func() time.Time { return envNow },
	}}
}

// appEntry is shop's entry with these values (name and repo set).
func appEntry(values map[string]any) *unstructured.Unstructured {
	u := entry("shop", mcloudLabels)
	u.SetUID(types.UID("shop-uid"))
	v := map[string]any{"name": "shop", "repo": "ann/shop", "owners": "ann,bo"}
	for k, x := range values {
		v[k] = x
	}
	u.Object["spec"] = map[string]any{"type": "Static", "defaultValues": v}
	return u
}

func (e *envsEnv) reconcile() ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "shop"}})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *envsEnv) entry(name string) (*unstructured.Unstructured, bool) {
	e.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	err := e.c.Get(context.Background(), types.NamespacedName{Namespace: RegistryNamespace, Name: name}, u)
	if err != nil {
		return nil, false
	}
	return u, true
}

func (e *envsEnv) preview(name string) (*v1.Preview, bool) {
	e.t.Helper()
	var p v1.Preview
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: name}, &p); err != nil {
		return nil, false
	}
	return &p, true
}

func (e *envsEnv) setValues(values map[string]any) {
	e.t.Helper()
	u, _ := e.entry("shop")
	cur := values
	unstructured.SetNestedMap(u.Object, cur, "spec", "defaultValues")
	if err := e.c.Update(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
}

func wantValues(t *testing.T, u *unstructured.Unstructured, want map[string]string) {
	t.Helper()
	got := values(u)
	if len(got) != len(want) {
		t.Errorf("%s: values %v, want %v", u.GetName(), got, want)
		return
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %s = %v, want %s", u.GetName(), k, got[k], w)
		}
	}
}

func TestEnvironmentsFromTheAppsEntry(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{
		"cpu": "500m", "buckets": "uploads,assets:public", "db": "true", "dbSize": "2Gi", "domains": "shop.example.com:web",
		"environments": `{"staging":{"cpu":"100m","db":"true","dbSize":"1Gi"},"qa":{"branch":"release/qa","buckets":"","db":"false","domains":"qa.example.com:web"}}`,
	}))
	e.reconcile()

	staging, ok := e.entry("shop--staging")
	if !ok {
		t.Fatal("no shop--staging")
	}
	wantValues(t, staging, map[string]string{
		"name": "shop--staging", "repo": "ann/shop", "owners": "ann,bo", "branch": "staging", "path": "deploy/env/staging",
		"cpu": "100m", "buckets": "uploads,assets:public", "db": "true", "dbSize": "1Gi",
	})
	l := staging.GetLabels()
	if l[appLabel] != "true" || l[EnvironmentOfLabel] != "shop" || l[EnvironmentLabel] != "staging" || l[EnvironmentKindLabel] != KindEnvironment || l[ManagedByLabel] != controllerManaged {
		t.Errorf("labels = %v", l)
	}
	qa, ok := e.entry("shop--qa")
	if !ok {
		t.Fatal("no shop--qa")
	}
	wantValues(t, qa, map[string]string{
		"name": "shop--qa", "repo": "ann/shop", "owners": "ann,bo", "branch": "release/qa", "path": "deploy/env/qa",
		"cpu": "500m", "domains": "qa.example.com:web",
	})

	// The app drops qa and gets a new owner: qa goes, staging follows.
	e.setValues(map[string]any{"name": "shop", "repo": "ann/shop", "owners": "ann,bo,cy", "environments": `{"staging":{}}`})
	e.reconcile()
	if _, ok := e.entry("shop--qa"); ok {
		t.Error("shop--qa is still there")
	}
	staging, _ = e.entry("shop--staging")
	wantValues(t, staging, map[string]string{
		"name": "shop--staging", "repo": "ann/shop", "owners": "ann,bo,cy", "branch": "staging", "path": "deploy/env/staging",
	})
}

// An entry without the environments key (an older mcloud rewrote it)
// keeps its environments; environments: {} removes them.
func TestEnvironmentsKeptWithoutTheKey(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{"environments": `{"staging":{}}`}))
	e.reconcile()
	e.setValues(map[string]any{"name": "shop", "repo": "ann/shop", "owners": "ann,bo"})
	e.reconcile()
	if _, ok := e.entry("shop--staging"); !ok {
		t.Fatal("shop--staging removed without environments: {}")
	}
	e.setValues(map[string]any{"name": "shop", "repo": "ann/shop", "owners": "ann,bo", "environments": "{}"})
	e.reconcile()
	if _, ok := e.entry("shop--staging"); ok {
		t.Error("shop--staging kept after environments: {}")
	}
}

func TestEnvironmentsOfADirectApp(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{"deploy": "direct", "repo": "", "environments": `{"staging":{}}`}))
	e.setValues(map[string]any{"name": "shop", "deploy": "direct", "owners": "ann", "environments": `{"staging":{}}`})
	e.reconcile()
	u, ok := e.entry("shop--staging")
	if !ok {
		t.Fatal("no shop--staging")
	}
	wantValues(t, u, map[string]string{"name": "shop--staging", "deploy": "direct", "owners": "ann"})
}

// After a rebuild an environment comes back as the same registration, so
// its buckets and database are its own again.
func TestEnvironmentKeepsItsRegistrationFromTheCopy(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{"environments": `{"staging":{}}`}))
	e.objects.put(registryBucket, backupKey("shop--staging"), "apiVersion: fluxcd.controlplane.io/v1\nkind: ResourceSetInputProvider\nmetadata:\n  name: shop--staging\n  namespace: flux-system\n  annotations:\n    "+RegistrationAnnotation+": old-id\n")
	e.reconcile()
	u, _ := e.entry("shop--staging")
	if got := u.GetAnnotations()[RegistrationAnnotation]; got != "old-id" {
		t.Errorf("registration = %q, want old-id", got)
	}
}

func TestEnvironmentsRefused(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{"environments": `{"pr-1":{}}`}))
	e.reconcile()
	u, _ := e.entry("shop")
	if msg := u.GetAnnotations()[EnvironmentsErrorAnnotation]; !strings.Contains(msg, "pull requests") {
		t.Errorf("error annotation = %q", msg)
	}
	if _, ok := e.entry("shop--pr-1"); ok {
		t.Error("made shop--pr-1")
	}

	e.r.MaxEnvironments = 1
	e.setValues(map[string]any{"name": "shop", "environments": `{"a":{},"b":{}}`})
	e.reconcile()
	u, _ = e.entry("shop")
	if msg := u.GetAnnotations()[EnvironmentsErrorAnnotation]; !strings.Contains(msg, "at most 1") {
		t.Errorf("error annotation = %q", msg)
	}
	if _, ok := e.entry("shop--b"); ok {
		t.Error("made a second environment over the limit")
	}

	e.setValues(map[string]any{"name": "shop", "environments": `{"a":{}}`})
	e.reconcile()
	u, _ = e.entry("shop")
	if msg, ok := u.GetAnnotations()[EnvironmentsErrorAnnotation]; ok {
		t.Errorf("error annotation stays: %q", msg)
	}
}

// pullRequests is shop's input provider listing these open pull requests
// (number to head commit).
func pullRequests(prs map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	u.SetNamespace(RegistryNamespace)
	u.SetName("shop" + pullRequestsSuffix)
	u.SetLabels(map[string]string{PullRequestsOfLabel: "shop"})
	var inputs []any
	for id, sha := range prs {
		inputs = append(inputs, map[string]any{"id": id, "sha": sha, "branch": "feature", "title": "whatever"})
	}
	u.Object["spec"] = map[string]any{"type": "GitHubPullRequest", "url": "https://github.com/ann/shop"}
	u.Object["status"] = map[string]any{
		"conditions":     []any{map[string]any{"type": "Ready", "status": "True"}},
		"exportedInputs": inputs,
	}
	return u
}

func TestPullRequestPreviews(t *testing.T) {
	e := newEnvsEnv(t,
		appEntry(map[string]any{"buckets": "uploads", "previews": "true", "previewTTL": "48h", "previewDB": "true"}),
		pullRequests(map[string]string{"7": "aaa"}),
	)
	res := e.reconcile()
	p, ok := e.preview("pr-7")
	if !ok {
		t.Fatal("no Preview pr-7")
	}
	if p.Spec.PullRequest != 7 || p.Spec.Revision != "aaa" || !p.Spec.ExpiresAt.Time.Equal(envNow.Add(48*time.Hour)) {
		t.Errorf("spec = %+v", p.Spec)
	}
	if p.Status.Phase != v1.PreviewActive || p.Status.Environment != "shop--pr-7" {
		t.Errorf("status = %+v", p.Status)
	}
	u, ok := e.entry("shop--pr-7")
	if !ok {
		t.Fatal("no shop--pr-7")
	}
	wantValues(t, u, map[string]string{
		"name": "shop--pr-7", "repo": "ann/shop", "owners": "ann,bo", "branch": "maxcloud/preview/7", "path": "deploy/k8s",
		"cpu": "100m", "memory": "512Mi", "pods": "10", "buckets": "uploads", "db": "true", "dbSize": "1Gi", "dbInstances": "1",
		"reclaim": "Delete",
	})
	if u.GetLabels()[EnvironmentKindLabel] != KindPreview {
		t.Errorf("labels = %v", u.GetLabels())
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 5*time.Minute {
		t.Errorf("requeue after %v", res.RequeueAfter)
	}

	// Two days on, no push: expired, the environment goes, the Preview
	// stays so the pull request doesn't get it back by itself.
	e.r.Now = func() time.Time { return envNow.Add(49 * time.Hour) }
	e.reconcile()
	if p, _ := e.preview("pr-7"); p.Status.Phase != v1.PreviewExpired {
		t.Errorf("phase = %s", p.Status.Phase)
	}
	if _, ok := e.entry("shop--pr-7"); ok {
		t.Error("shop--pr-7 outlived its preview")
	}

	// A push: back, for another 48 hours.
	prov := pullRequests(map[string]string{"7": "bbb"})
	cur, _ := e.entry("shop" + pullRequestsSuffix)
	prov.SetResourceVersion(cur.GetResourceVersion())
	if err := e.c.Update(context.Background(), prov); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	p, _ = e.preview("pr-7")
	if p.Spec.Revision != "bbb" || !p.Spec.ExpiresAt.Time.Equal(envNow.Add(97*time.Hour)) || p.Status.Phase != v1.PreviewActive {
		t.Errorf("after a push: spec %+v, phase %s", p.Spec, p.Status.Phase)
	}
	if _, ok := e.entry("shop--pr-7"); !ok {
		t.Error("shop--pr-7 didn't come back")
	}

	// Closed: the Preview and the environment go.
	prov = pullRequests(nil)
	cur, _ = e.entry("shop" + pullRequestsSuffix)
	prov.SetResourceVersion(cur.GetResourceVersion())
	e.c.Update(context.Background(), prov)
	e.reconcile()
	if _, ok := e.preview("pr-7"); ok {
		t.Error("Preview pr-7 outlived its pull request")
	}
	if _, ok := e.entry("shop--pr-7"); ok {
		t.Error("shop--pr-7 outlived its pull request")
	}
}

// A provider that can't reach GitHub changes nothing.
func TestPullRequestPreviewsKeptWhileGitHubIsDown(t *testing.T) {
	prov := pullRequests(nil)
	prov.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False"}}
	expires := metav1.NewTime(envNow.Add(time.Hour))
	e := newEnvsEnv(t, appEntry(map[string]any{"previews": "true"}), prov,
		&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "pr-3"}, Spec: v1.PreviewSpec{PullRequest: 3, Revision: "x", ExpiresAt: &expires}})
	e.reconcile()
	if _, ok := e.preview("pr-3"); !ok {
		t.Error("Preview pr-3 deleted while the provider isn't Ready")
	}
}

// A provider for another repo, or not a pull request list, is ignored.
func TestPullRequestPreviewsOnlyFromTheAppsProvider(t *testing.T) {
	other := pullRequests(map[string]string{"5": "x"})
	other.Object["spec"] = map[string]any{"type": "GitHubPullRequest", "url": "https://github.com/eve/shop"}
	e := newEnvsEnv(t, appEntry(map[string]any{"previews": "true"}), other)
	e.reconcile()
	if _, ok := e.preview("pr-5"); ok {
		t.Error("made a preview from another repo's pull requests")
	}
	static := pullRequests(map[string]string{"5": "x"})
	static.Object["spec"] = map[string]any{"type": "Static"}
	e = newEnvsEnv(t, appEntry(map[string]any{"previews": "true"}), static)
	e.reconcile()
	if _, ok := e.preview("pr-5"); ok {
		t.Error("made a preview from a Static provider")
	}
}

func TestWorkingTreePreviews(t *testing.T) {
	soon := metav1.NewTime(envNow.Add(2 * time.Minute))
	gone := metav1.NewTime(envNow.Add(-time.Minute))
	older := metav1.NewTime(envNow.Add(-2 * time.Hour))
	e := newEnvsEnv(t, appEntry(map[string]any{"previews": "true", "previewMax": "1"}),
		&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ann", CreationTimestamp: older}, Spec: v1.PreviewSpec{ExpiresAt: &soon}},
		&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "bo", CreationTimestamp: metav1.NewTime(envNow.Add(-time.Hour))}, Spec: v1.PreviewSpec{ExpiresAt: &soon}},
		&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "old"}, Spec: v1.PreviewSpec{ExpiresAt: &gone}},
	)
	res := e.reconcile()
	u, ok := e.entry("shop--ann")
	if !ok {
		t.Fatal("no shop--ann")
	}
	wantValues(t, u, map[string]string{
		"name": "shop--ann", "deploy": "direct", "owners": "ann,bo", "cpu": "100m", "memory": "512Mi", "pods": "10", "reclaim": "Delete",
	})
	if p, _ := e.preview("bo"); p.Status.Phase != v1.PreviewWaiting || p.Status.Conditions[0].Reason != "OverLimit" {
		t.Errorf("bo: %+v", p.Status)
	}
	if _, ok := e.entry("shop--bo"); ok {
		t.Error("shop--bo over the limit")
	}
	if _, ok := e.preview("old"); ok {
		t.Error("an expired working-tree preview is still there")
	}
	if res.RequeueAfter != 2*time.Minute {
		t.Errorf("requeue after %v, want the next expiry (2m)", res.RequeueAfter)
	}

	// The platform's limit counts other apps' previews.
	other := entry("cafe--x", map[string]string{appLabel: "true", EnvironmentOfLabel: "cafe", EnvironmentKindLabel: KindPreview})
	if err := e.c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	e.r.MaxPreviews = 1
	e.reconcile()
	if _, ok := e.entry("shop--ann"); ok {
		t.Error("shop--ann kept beyond the platform's limit")
	}

	// Previews off: they wait.
	e.r.MaxPreviews = 0
	e.setValues(map[string]any{"name": "shop", "owners": "ann"})
	e.reconcile()
	if p, _ := e.preview("ann"); p.Status.Phase != v1.PreviewWaiting || p.Status.Conditions[0].Reason != "PreviewsOff" {
		t.Errorf("ann: %+v", p.Status)
	}
}

// Deleting the app deletes its environments.
func TestEnvironmentsGoWithTheApp(t *testing.T) {
	e := newEnvsEnv(t, appEntry(map[string]any{"environments": `{"staging":{}}`}))
	e.reconcile()
	if _, ok := e.entry("shop--staging"); !ok {
		t.Fatal("no shop--staging")
	}
	u, _ := e.entry("shop")
	if err := e.c.Delete(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if _, ok := e.entry("shop--staging"); ok {
		t.Error("shop--staging outlived the app")
	}
}

func TestAppOf(t *testing.T) {
	staging := entry("shop--staging", map[string]string{appLabel: "true", EnvironmentOfLabel: "shop"})
	for _, tc := range []struct {
		o    client.Object
		want string
	}{
		{entry("shop", mcloudLabels), "shop"},
		{staging, "shop"},
		{pullRequests(nil), "shop"},
		{&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "x"}}, "shop"},
		// A Preview in an environment's namespace makes nothing.
		{&v1.Preview{ObjectMeta: metav1.ObjectMeta{Namespace: "shop--staging", Name: "x"}}, ""},
		{entry("other", nil), ""},
	} {
		if got := appOf(tc.o); got != tc.want {
			t.Errorf("appOf(%s/%s) = %q, want %q", tc.o.GetNamespace(), tc.o.GetName(), got, tc.want)
		}
	}
}

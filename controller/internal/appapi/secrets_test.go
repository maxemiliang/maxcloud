package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
	"github.com/maxemiliang/maxcloud/controller/internal/openbao"
)

// memKV is OpenBao's KV in memory.
type memKV struct {
	mu       sync.Mutex
	secrets  map[string]*openbao.Secret
	conflict int // this many writes fail with a CAS mismatch first
	writes   int
}

func (m *memKV) Read(_ context.Context, path string) (*openbao.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[path]
	if !ok {
		return nil, openbao.ErrNotFound
	}
	cp := &openbao.Secret{Version: s.Version, Created: s.Created, Data: map[string]string{}}
	for k, v := range s.Data {
		cp.Data[k] = v
	}
	return cp, nil
}

func (m *memKV) Write(_ context.Context, path string, data map[string]string, cas int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflict > 0 {
		m.conflict--
		return 0, openbao.ErrCASMismatch
	}
	cur := 0
	if s, ok := m.secrets[path]; ok {
		cur = s.Version
	}
	if cas >= 0 && cas != cur {
		return 0, openbao.ErrCASMismatch
	}
	m.writes++
	m.secrets[path] = &openbao.Secret{Data: data, Version: cur + 1, Created: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	return cur + 1, nil
}

// fakeESO answers for ExternalSecrets app-secrets: it syncs (a new
// syncedResourceVersion, Ready) after a force-sync, unless stuck.
type fakeESO struct {
	mu      sync.Mutex
	exists  map[string]bool
	stuck   map[string]bool
	forced  map[string]string
	patches int
}

func (e *fakeESO) object(ns string) map[string]any {
	synced, ready := "1-old", map[string]any{"type": "Ready", "status": "True", "reason": "SecretSynced"}
	if e.stuck[ns] {
		ready = map[string]any{"type": "Ready", "status": "False", "reason": "SecretSyncedError", "message": "could not get secret data from provider"}
	} else if f := e.forced[ns]; f != "" {
		synced = "1-" + f
	}
	return map[string]any{
		"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret",
		"metadata": map[string]any{"name": AppSecretsName, "namespace": ns},
		"status":   map[string]any{"syncedResourceVersion": synced, "conditions": []any{ready}},
	}
}

var esGR = schema.GroupResource{Group: "external-secrets.io", Resource: "externalsecrets"}

func newSecretsHandler(t *testing.T) (*Handler, *memKV, *fakeESO, *[]authzv1.ResourceAttributes) {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	ns := func(name string, tenant bool) *corev1.Namespace {
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if tenant {
			n.Labels = map[string]string{TenantLabel: "true"}
		}
		return n
	}
	eso := &fakeESO{exists: map[string]bool{"shop": true, "other": true}, stuck: map[string]bool{}, forced: map[string]string{}}
	var sars []authzv1.ResourceAttributes
	var mu sync.Mutex
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(ns("shop", true), ns("other", true), ns("bare", true), ns("kube-system", false)).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authnv1.TokenReview:
				users := map[string]string{"ann-token": "authelia:ann", "vic-token": "authelia:vic", "admin-token": "authelia:root"}
				if u, ok := users[o.Spec.Token]; ok {
					o.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: u}}
				}
				return nil
			case *authzv1.SubjectAccessReview:
				ra := o.Spec.ResourceAttributes
				mu.Lock()
				sars = append(sars, *ra)
				mu.Unlock()
				// ann owns shop; vic may only read its secrets; root is an admin.
				o.Status.Allowed = o.Spec.User == "authelia:root" ||
					o.Spec.User == "authelia:ann" && ra.Namespace == "shop" ||
					o.Spec.User == "authelia:vic" && ra.Namespace == "shop" && ra.Verb == "get" && ra.Name == AppSecretsName
				return nil
			}
			return errors.New("unexpected create")
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return c.Get(ctx, key, obj, opts...)
			}
			eso.mu.Lock()
			defer eso.mu.Unlock()
			if !eso.exists[key.Namespace] || key.Name != AppSecretsName {
				return apierrors.NewNotFound(esGR, key.Name)
			}
			u.Object = eso.object(key.Namespace)
			return nil
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			u := obj.(*unstructured.Unstructured)
			b, _ := patch.Data(obj)
			var p struct {
				Metadata struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(b, &p); err != nil || p.Metadata.Annotations[ForceSyncAnnotation] == "" {
				t.Errorf("patch %s", b)
			}
			eso.mu.Lock()
			defer eso.mu.Unlock()
			eso.patches++
			// The answer is the object as it was: ESO hasn't synced yet.
			u.Object = eso.object(u.GetNamespace())
			if !eso.stuck[u.GetNamespace()] {
				eso.forced[u.GetNamespace()] = p.Metadata.Annotations[ForceSyncAnnotation]
			}
			return nil
		},
	})
	kv := &memKV{secrets: map[string]*openbao.Secret{
		"shop/app-secrets": {Data: map[string]string{"STRIPE_KEY": "sk_live_1", "OLD": "x"}, Version: 3, Created: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)},
	}}
	h := &Handler{Reviewer: &caller.Reviewer{Client: c}, Reader: c, Writer: c, Secrets: kv, SyncTimeout: 200 * time.Millisecond, SyncPoll: 10 * time.Millisecond}
	return h, kv, eso, &sars
}

func do(h http.Handler, method, token, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestSecretsAccess(t *testing.T) {
	h, kv, _, sars := newSecretsHandler(t)
	for _, c := range []struct {
		method, token, path, body string
		want                      int
	}{
		{"GET", "", "/api/v1/apps/shop/secrets", "", http.StatusUnauthorized},
		{"GET", "ann-token", "/api/v1/apps/other/secrets", "", http.StatusForbidden},
		{"PUT", "ann-token", "/api/v1/apps/other/secrets/A", `{"value":"v"}`, http.StatusForbidden},
		{"PUT", "vic-token", "/api/v1/apps/shop/secrets/A", `{"value":"v"}`, http.StatusForbidden},
		{"DELETE", "vic-token", "/api/v1/apps/shop/secrets/OLD", "", http.StatusForbidden},
		{"GET", "admin-token", "/api/v1/apps/kube-system/secrets", "", http.StatusNotFound},
		{"PUT", "ann-token", "/api/v1/apps/shop/secrets/lower", `{"value":"v"}`, http.StatusBadRequest},
		{"PUT", "ann-token", "/api/v1/apps/shop/secrets/AWS_REGION", `{"value":"v"}`, http.StatusBadRequest},
		{"PUT", "ann-token", "/api/v1/apps/shop/secrets/PORT", `{"value":"v"}`, http.StatusBadRequest},
		{"PUT", "ann-token", "/api/v1/apps/shop/secrets/A", `{"value":""}`, http.StatusBadRequest},
		{"PUT", "ann-token", "/api/v1/apps/shop/secrets/A", `not json`, http.StatusBadRequest},
		{"DELETE", "ann-token", "/api/v1/apps/shop/secrets/NOPE", "", http.StatusNotFound},
		{"GET", "vic-token", "/api/v1/apps/shop/secrets", "", http.StatusOK},
	} {
		w := do(h, c.method, c.token, c.path, c.body)
		if w.Code != c.want {
			t.Errorf("%s %s as %q: %d %s, want %d", c.method, c.path, c.token, w.Code, w.Body, c.want)
		}
	}
	if kv.writes != 0 {
		t.Errorf("%d writes from refused requests", kv.writes)
	}
	// Every check was about the one Secret, by name.
	for _, ra := range *sars {
		if ra.Resource != "secrets" || ra.Name != AppSecretsName || (ra.Verb != "get" && ra.Verb != "update") {
			t.Errorf("review %+v", ra)
		}
	}
}

func TestSecretsList(t *testing.T) {
	h, _, _, _ := newSecretsHandler(t)
	w := do(h, "GET", "ann-token", "/api/v1/apps/shop/secrets", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sk_live_1") {
		t.Fatalf("a value in the list: %s", w.Body)
	}
	var got SecretsResponse
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.App != "shop" || got.Version != 3 || got.Updated == nil || len(got.Secrets) != 2 || got.Secrets[0].Name != "OLD" || got.Secrets[1].Name != "STRIPE_KEY" {
		t.Errorf("%+v", got)
	}
	// An app without any secrets yet.
	w = do(h, "GET", "admin-token", "/api/v1/apps/other/secrets", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"secrets":[]`) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestSecretsSetAndUnset(t *testing.T) {
	h, kv, eso, _ := newSecretsHandler(t)
	w := do(h, "PUT", "ann-token", "/api/v1/apps/shop/secrets/NEW_KEY", `{"value":"s3cret"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var ch SecretChange
	json.Unmarshal(w.Body.Bytes(), &ch)
	if !ch.Synced || ch.Version != 4 || ch.Name != "NEW_KEY" {
		t.Errorf("%+v", ch)
	}
	s, _ := kv.Read(context.Background(), "shop/app-secrets")
	if s.Data["NEW_KEY"] != "s3cret" || s.Data["STRIPE_KEY"] != "sk_live_1" {
		t.Errorf("kv %+v", s.Data)
	}
	if eso.patches != 1 {
		t.Errorf("%d force-syncs", eso.patches)
	}

	w = do(h, "DELETE", "ann-token", "/api/v1/apps/shop/secrets/OLD", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s, _ = kv.Read(context.Background(), "shop/app-secrets")
	if _, ok := s.Data["OLD"]; ok || s.Version != 5 {
		t.Errorf("kv after unset %+v", s)
	}

	// An app's first secret, and someone else writing in between.
	kv.conflict = 1
	w = do(h, "PUT", "admin-token", "/api/v1/apps/other/secrets/FIRST", `{"value":"1"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if s, _ := kv.Read(context.Background(), "other/app-secrets"); s.Version != 1 || s.Data["FIRST"] != "1" {
		t.Errorf("first secret %+v", s)
	}
}

// When ESO doesn't sync in time, or there's no ExternalSecret yet: OpenBao
// has the value, and the answer is 202 with why.
func TestSecretsNotSynced(t *testing.T) {
	h, kv, eso, _ := newSecretsHandler(t)
	eso.stuck["shop"] = true
	w := do(h, "PUT", "ann-token", "/api/v1/apps/shop/secrets/A", `{"value":"1"}`)
	var ch SecretChange
	json.Unmarshal(w.Body.Bytes(), &ch)
	if w.Code != http.StatusAccepted || ch.Synced || !strings.Contains(ch.Message, "could not get secret data") {
		t.Errorf("stuck: %d %s", w.Code, w.Body)
	}
	if s, _ := kv.Read(context.Background(), "shop/app-secrets"); s.Data["A"] != "1" {
		t.Error("not written")
	}

	w = do(h, "PUT", "admin-token", "/api/v1/apps/bare/secrets/A", `{"value":"1"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "no ExternalSecret") {
		t.Errorf("no ExternalSecret: %d %s", w.Code, w.Body)
	}
}

func TestSecretsFeature(t *testing.T) {
	if f := (&Handler{Loki: "http://loki"}).Features(); len(f) != 1 {
		t.Errorf("%v", f)
	}
	h, _, _, _ := newSecretsHandler(t)
	if f := h.Features(); len(f) != 1 || f[0] != FeatureSecrets {
		t.Errorf("%v", f)
	}
	// Without OpenBao, the routes aren't there.
	h.Secrets = nil
	h.mux = nil
	if w := do(h, "GET", "ann-token", "/api/v1/apps/shop/secrets", ""); w.Code != http.StatusNotFound {
		t.Errorf("%d", w.Code)
	}
}

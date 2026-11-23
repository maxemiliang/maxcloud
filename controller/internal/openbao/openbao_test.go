package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBao is enough of OpenBao: Kubernetes login and a KV v2 mount "kv".
type fakeBao struct {
	mu       sync.Mutex
	logins   int
	valid    map[string]bool // tokens it accepts
	secrets  map[string][]map[string]any
	deleted  map[string]bool // current version deleted
	lease    int
	lastJWT  string
	requests []string
}

func newFake() *fakeBao {
	return &fakeBao{valid: map[string]bool{}, secrets: map[string][]map[string]any{}, deleted: map[string]bool{}, lease: 3600}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/v1/auth/kubernetes/login" {
		var in struct{ Role, JWT string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Role != "controller" || in.JWT == "" {
			writeJSON(w, 400, map[string]any{"errors": []string{"invalid role or jwt"}})
			return
		}
		f.logins++
		f.lastJWT = in.JWT
		tok := "tok-" + string(rune('a'+f.logins))
		f.valid[tok] = true
		writeJSON(w, 200, map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": f.lease}})
		return
	}
	if !f.valid[r.Header.Get("X-Vault-Token")] {
		writeJSON(w, 403, map[string]any{"errors": []string{"permission denied"}})
		return
	}
	if p, ok := strings.CutPrefix(r.URL.Path, "/v1/kv/data/"); ok {
		vs := f.secrets[p]
		switch r.Method {
		case http.MethodGet:
			if len(vs) == 0 {
				writeJSON(w, 404, map[string]any{"errors": []string{}})
				return
			}
			meta := map[string]any{"version": len(vs), "created_time": "2026-11-22T10:00:00Z"}
			if f.deleted[p] {
				writeJSON(w, 404, map[string]any{"data": map[string]any{"data": nil, "metadata": meta}})
				return
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"data": vs[len(vs)-1], "metadata": meta}})
		case http.MethodPost:
			var in struct {
				Data    map[string]any `json:"data"`
				Options *struct {
					CAS int `json:"cas"`
				} `json:"options"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Data == nil {
				writeJSON(w, 400, map[string]any{"errors": []string{"no data provided"}})
				return
			}
			if in.Options != nil && in.Options.CAS != len(vs) {
				writeJSON(w, 400, map[string]any{"errors": []string{"check-and-set parameter did not match the current version"}})
				return
			}
			f.secrets[p] = append(vs, in.Data)
			delete(f.deleted, p)
			writeJSON(w, 200, map[string]any{"data": map[string]any{"version": len(vs) + 1, "created_time": "2026-11-22T10:00:00Z"}})
		}
		return
	}
	if p, ok := strings.CutPrefix(r.URL.Path, "/v1/kv/metadata/"); ok {
		vs, found := f.secrets[p]
		if !found {
			writeJSON(w, 404, map[string]any{"errors": []string{}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"data": map[string]any{"current_version": len(vs), "updated_time": "2026-11-22T10:00:00Z"}})
		case http.MethodDelete:
			delete(f.secrets, p)
			w.WriteHeader(204)
		}
		return
	}
	writeJSON(w, 404, map[string]any{"errors": []string{}})
}

func newClient(t *testing.T, f *fakeBao) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("sa-jwt\n"), 0o600)
	return &Client{Addr: srv.URL, HTTP: srv.Client(), AuthMount: "kubernetes", Role: "controller", KVMount: "kv", TokenFile: tokenFile}
}

func TestReadWrite(t *testing.T) {
	f := newFake()
	c := newClient(t, f)
	ctx := context.Background()

	if _, err := c.Read(ctx, "shop/app-secrets"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read of nothing: %v", err)
	}
	if ok, err := c.Exists(ctx, "shop/app-secrets"); ok || err != nil {
		t.Fatalf("exists: %v %v", ok, err)
	}
	v, err := c.Write(ctx, "shop/app-secrets", map[string]string{"A": "1"}, 0)
	if err != nil || v != 1 {
		t.Fatalf("create: %d %v", v, err)
	}
	// cas 0 again: it exists now.
	if _, err := c.Write(ctx, "shop/app-secrets", map[string]string{"A": "2"}, 0); !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("second create: %v", err)
	}
	if v, err := c.Write(ctx, "shop/app-secrets", map[string]string{"A": "2", "B": "x"}, 1); err != nil || v != 2 {
		t.Fatalf("update: %d %v", v, err)
	}
	s, err := c.Read(ctx, "shop/app-secrets")
	if err != nil || s.Version != 2 || s.Data["A"] != "2" || s.Data["B"] != "x" || s.Created.IsZero() {
		t.Fatalf("read: %+v %v", s, err)
	}
	// An empty secret is a secret (ESO then makes an empty Secret).
	if _, err := c.Write(ctx, "new/app-secrets", nil, 0); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if ok, _ := c.Exists(ctx, "new/app-secrets"); !ok {
		t.Fatal("empty secret doesn't exist")
	}
	if err := c.DeleteMetadata(ctx, "shop/app-secrets"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteMetadata(ctx, "shop/app-secrets"); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if f.logins != 1 || f.lastJWT != "sa-jwt" {
		t.Errorf("logins %d, jwt %q", f.logins, f.lastJWT)
	}
}

// A deleted current version: not found, but its version is there for a
// check-and-set write.
func TestReadDeletedVersion(t *testing.T) {
	f := newFake()
	c := newClient(t, f)
	ctx := context.Background()
	c.Write(ctx, "shop/app-secrets", map[string]string{"A": "1"}, 0)
	f.deleted["shop/app-secrets"] = true
	s, err := c.Read(ctx, "shop/app-secrets")
	if !errors.Is(err, ErrNotFound) || s == nil || s.Version != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}

// A token OpenBao no longer accepts: one fresh login and the request
// again. A policy that denies stays denied.
func TestReloginOn403(t *testing.T) {
	f := newFake()
	c := newClient(t, f)
	ctx := context.Background()
	if _, err := c.Write(ctx, "shop/app-secrets", map[string]string{"A": "1"}, -1); err != nil {
		t.Fatal(err)
	}
	f.valid = map[string]bool{} // revoked
	if _, err := c.Read(ctx, "shop/app-secrets"); err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if f.logins != 2 {
		t.Errorf("logins %d, want 2", f.logins)
	}

	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			writeJSON(w, 200, map[string]any{"auth": map[string]any{"client_token": "t", "lease_duration": 60}})
			return
		}
		writeJSON(w, 403, map[string]any{"errors": []string{"permission denied"}})
	}))
	defer deny.Close()
	c.Addr, c.HTTP = deny.URL, deny.Client()
	if _, err := c.Read(ctx, "kube-system/x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("denied: %v", err)
	}
}

// Two thirds into the lease, the next request logs in again.
func TestLoginRenewal(t *testing.T) {
	f := newFake()
	f.lease = 60
	c := newClient(t, f)
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	c.Exists(ctx, "a")
	now = now.Add(30 * time.Second)
	c.Exists(ctx, "a")
	if f.logins != 1 {
		t.Fatalf("logins %d after 30s", f.logins)
	}
	now = now.Add(11 * time.Second)
	c.Exists(ctx, "a")
	if f.logins != 2 {
		t.Fatalf("logins %d after 41s", f.logins)
	}
}

func TestNewReadsCA(t *testing.T) {
	if _, err := New("https://openbao.openbao.svc:8200", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing CA file: no error")
	}
	bad := filepath.Join(t.TempDir(), "ca.crt")
	os.WriteFile(bad, []byte("not pem"), 0o600)
	if _, err := New("https://openbao.openbao.svc:8200", bad); err == nil {
		t.Error("bad CA file: no error")
	}
	c, err := New("https://openbao.openbao.svc:8200/", "")
	if err != nil || c.Addr != "https://openbao.openbao.svc:8200" || c.KVMount != "apps" || c.Role != "controller" {
		t.Errorf("%+v %v", c, err)
	}
}

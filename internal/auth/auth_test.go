package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func jwt(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256"}) + "." + enc(claims) + ".sig"
}

// provider is a fake OIDC provider: discovery, device authorization, token.
type provider struct {
	*httptest.Server
	polls     atomic.Int32
	challenge string // the PKCE challenge the browser flow sent
	refreshed atomic.Int32
}

func newProvider(t *testing.T) *provider {
	p := &provider{}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	issue := func(w http.ResponseWriter, refresh string) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": refresh,
			"id_token": jwt(map[string]any{
				"exp": time.Now().Add(time.Hour).Unix(), "preferred_username": "max", "groups": []string{"admins"},
			}),
		})
	}
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                        p.URL,
			"authorization_endpoint":        p.URL + "/authorize",
			"token_endpoint":                p.URL + "/token",
			"device_authorization_endpoint": p.URL + "/device",
		})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "kubernetes" {
			http.Error(w, "client", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri": p.URL + "/verify",
			"verification_uri_complete": p.URL + "/verify?code=ABCD-EFGH", "interval": 1, "expires_in": 60,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if p.polls.Add(1) == 1 { // the user hasn't approved yet
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			issue(w, "rt-1")
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			issue(w, "rt-browser")
		case "refresh_token":
			if r.Form.Get("refresh_token") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			p.refreshed.Add(1)
			issue(w, "") // not rotated
		default:
			http.Error(w, "grant", http.StatusBadRequest)
		}
	})
	return p
}

func TestDeviceLoginRefreshAndCache(t *testing.T) {
	p := newProvider(t)
	cfg := Config{Issuer: p.URL, CacheDir: t.TempDir()}
	var shown string
	tok, err := DeviceLogin(context.Background(), cfg, func(u, code string) { shown = u + " " + code })
	if err != nil {
		t.Fatal(err)
	}
	if shown == "" || p.polls.Load() < 2 {
		t.Errorf("prompt %q, polls %d (want the pending answer waited out)", shown, p.polls.Load())
	}
	if tok.User != "max" || len(tok.Groups) != 1 || tok.RefreshToken != "rt-1" || !tok.Valid(time.Now()) {
		t.Errorf("tokens = %+v", tok)
	}

	if err := Save(cfg, tok); err != nil {
		t.Fatal(err)
	}
	path, _ := cachePath(cfg)
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("cache %s: %v %v (want 0600)", path, fi.Mode(), err)
	}
	back, err := Load(cfg)
	if err != nil || back.IDToken != tok.IDToken {
		t.Fatalf("load: %+v %v", back, err)
	}

	r, err := Refresh(context.Background(), cfg, back)
	if err != nil {
		t.Fatal(err)
	}
	if p.refreshed.Load() != 1 || r.RefreshToken != "rt-1" {
		t.Errorf("refresh: %d calls, refresh token %q (an unrotated one is kept)", p.refreshed.Load(), r.RefreshToken)
	}

	if err := Forget(cfg); err != nil {
		t.Fatal(err)
	}
	if gone, _ := Load(cfg); gone != nil {
		t.Error("tokens still cached after Forget")
	}
}

func TestBrowserLogin(t *testing.T) {
	p := newProvider(t)
	// Listen on a free port instead of the fixed one; the "browser" is
	// sent there.
	var addr string
	old := listen
	listen = func(string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if ln != nil {
			addr = ln.Addr().String()
		}
		return ln, err
	}
	t.Cleanup(func() { listen = old })

	cfg := Config{Issuer: p.URL, CacheDir: t.TempDir()}
	tok, err := BrowserLogin(context.Background(), cfg, func(authURL string) {
		u, _ := url.Parse(authURL)
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "http://"+CallbackAddr+CallbackPath {
			t.Errorf("auth URL %s", authURL)
		}
		p.challenge = q.Get("code_challenge")
		go func() {
			// A stray request with the wrong state is ignored.
			http.Get("http://" + addr + CallbackPath + "?code=evil&state=wrong")
			http.Get("http://" + addr + CallbackPath + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt-browser" || tok.User != "max" {
		t.Errorf("tokens = %+v", tok)
	}
}

func TestClaims(t *testing.T) {
	if _, err := Claims("not-a-jwt"); err == nil {
		t.Error("want an error")
	}
	c, err := Claims(jwt(map[string]any{"exp": 42, "preferred_username": "max"}))
	if err != nil || c.Exp != 42 || c.PreferredUsername != "max" {
		t.Errorf("claims = %+v %v", c, err)
	}
}

func TestCachePathPerIssuer(t *testing.T) {
	a, _ := cachePath(Config{Issuer: "https://a.example", CacheDir: "/x"})
	b, _ := cachePath(Config{Issuer: "https://b.example", CacheDir: "/x"})
	if a == b || filepath.Dir(a) != "/x" {
		t.Errorf("paths %s %s", a, b)
	}
}

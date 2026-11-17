package gateway

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const (
	testIssuer   = "https://auth.example.test"
	testAudience = "kubernetes"
)

// token is an unsigned JWT with these claims: the gateway reads claims, and
// leaves the signature to the API server.
func token(t *testing.T, claims map[string]any) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(body) + "." + enc([]byte("sig"))
}

func login(t *testing.T, sub string) string {
	return token(t, map[string]any{"iss": testIssuer, "aud": []string{testAudience}, "sub": sub, "exp": time.Now().Add(time.Hour).Unix()})
}

// apiServer is a stand-in for the API server: it knows users by token,
// answers SelfSubjectReviews, echoes other requests, and upgrades /exec to
// a raw stream that echoes lines.
type apiServer struct {
	*httptest.Server
	users   map[string]string // token -> username
	reviews atomic.Int32
	seen    atomic.Pointer[http.Request]
}

func newAPIServer(t *testing.T, users map[string]string) *apiServer {
	a := &apiServer{users: users}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, `{"kind":"Status","code":401}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/apis/authentication.k8s.io/v1/selfsubjectreviews":
			a.reviews.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"status":{"userInfo":{"username":%q}}}`, user)
		case r.URL.Path == "/exec":
			conn, rw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
			rw.Flush()
			for {
				line, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				rw.WriteString("echo " + line)
				rw.Flush()
			}
		default:
			a.seen.Store(r.Clone(r.Context()))
			fmt.Fprintf(w, "%s %s as %s", r.Method, r.URL.RequestURI(), user)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

func newGateway(t *testing.T, a *apiServer) (*Server, *httptest.Server) {
	t.Helper()
	up, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Upstream: up, Transport: http.DefaultTransport, Issuer: testIssuer, Audience: testAudience, UserPrefix: "authelia:"}
	front := httptest.NewServer(s)
	t.Cleanup(front.Close)
	return s, front
}

func get(t *testing.T, url, token string, header ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestProxiesAPersonsLogin(t *testing.T) {
	ann := login(t, "ann")
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	_, front := newGateway(t, a)

	code, body := get(t, front.URL+"/api/v1/namespaces/shop/pods?watch=true", ann,
		"X-Remote-User", "system:admin", "X-Remote-Group", "system:masters", "Impersonate-User", "bob")
	if code != http.StatusOK || body != "GET /api/v1/namespaces/shop/pods?watch=true as authelia:ann" {
		t.Fatalf("got %d %q", code, body)
	}
	seen := a.seen.Load()
	if got := seen.Header.Get("Authorization"); got != "Bearer "+ann {
		t.Errorf("the API server got Authorization %q, want the caller's own token", got)
	}
	// A front proxy's identity headers never get through; impersonation is
	// the caller's own request, for the API server to allow or refuse.
	if seen.Header.Get("X-Remote-User") != "" || seen.Header.Get("X-Remote-Group") != "" {
		t.Errorf("X-Remote-* headers reached the API server: %v", seen.Header)
	}
	if seen.Header.Get("Impersonate-User") != "bob" {
		t.Errorf("Impersonate-User didn't reach the API server")
	}
	if seen.Header.Get("X-Forwarded-For") == "" {
		t.Errorf("no X-Forwarded-For for the audit log")
	}

	// The same token again isn't put to the API server a second time.
	get(t, front.URL+"/version", ann)
	if n := a.reviews.Load(); n != 1 {
		t.Errorf("%d SelfSubjectReviews for one token, want 1", n)
	}
}

func TestRefusesWithoutAskingTheAPIServer(t *testing.T) {
	a := newAPIServer(t, nil)
	_, front := newGateway(t, a)
	hour := time.Now().Add(time.Hour).Unix()
	for name, tok := range map[string]string{
		"no token":         "",
		"not a JWT":        "sha256~abcdef",
		"another issuer":   token(t, map[string]any{"iss": "https://kubernetes.default.svc", "aud": testAudience, "exp": hour}),
		"another audience": token(t, map[string]any{"iss": testIssuer, "aud": []string{"grafana"}, "exp": hour}),
		"expired":          token(t, map[string]any{"iss": testIssuer, "aud": testAudience, "exp": time.Now().Add(-time.Minute).Unix()}),
		"no expiry":        token(t, map[string]any{"iss": testIssuer, "aud": testAudience}),
	} {
		code, body := get(t, front.URL+"/api", tok)
		if code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, code)
		}
		var status struct{ Kind, Reason string }
		if err := json.Unmarshal([]byte(body), &status); err != nil || status.Kind != "Status" || status.Reason != "Unauthorized" {
			t.Errorf("%s: body %q isn't a Status", name, body)
		}
	}
	if n := a.reviews.Load(); n != 0 {
		t.Errorf("the API server was asked %d times, want 0", n)
	}
	if a.seen.Load() != nil {
		t.Error("a request reached the API server")
	}
}

func TestRefusesWhatTheAPIServerDoesnt(t *testing.T) {
	forged := login(t, "ann") // right claims, but the API server doesn't know it
	robot := login(t, "robot")
	a := newAPIServer(t, map[string]string{robot: "system:serviceaccount:shop:default"})
	_, front := newGateway(t, a)

	for name, tok := range map[string]string{"forged": forged, "not a person": robot} {
		for range 3 {
			if code, _ := get(t, front.URL+"/api", tok); code != http.StatusUnauthorized {
				t.Errorf("%s: got %d, want 401", name, code)
			}
		}
	}
	// One question per token: the refusal is remembered. (The forged one
	// never gets as far as a review: the API server answers 401 first.)
	if n := a.reviews.Load(); n != 1 {
		t.Errorf("%d SelfSubjectReviews, want 1", n)
	}
	if a.seen.Load() != nil {
		t.Error("a request reached the API server")
	}
}

func TestThrottlesUnknownTokens(t *testing.T) {
	ann := login(t, "ann")
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	s, front := newGateway(t, a)
	s.Verifications = rate.NewLimiter(0, 1) // one, then no more

	if code, _ := get(t, front.URL+"/api", ann); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if code, _ := get(t, front.URL+"/api", login(t, "mallory")); code != http.StatusTooManyRequests {
		t.Errorf("an unknown token past the limit: got %d, want 429", code)
	}
	// Someone already let in isn't held up by it.
	if code, _ := get(t, front.URL+"/api", ann); code != http.StatusOK {
		t.Errorf("a known token past the limit: got %d, want 200", code)
	}
}

func TestAPIServerDown(t *testing.T) {
	ann := login(t, "ann")
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	_, front := newGateway(t, a)
	a.Close()
	if code, _ := get(t, front.URL+"/api", ann); code != http.StatusBadGateway {
		t.Errorf("got %d, want 502", code)
	}
}

// exec and port-forward upgrade the connection (SPDY): the gateway has to
// pass the upgrade through and carry the stream both ways.
func TestUpgrade(t *testing.T) {
	ann := login(t, "ann")
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	_, front := newGateway(t, a)

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "POST /exec HTTP/1.1\r\nHost: gateway\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", ann)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "SPDY/3.1" {
		t.Fatalf("got %s (Upgrade %q), want 101 SPDY/3.1", resp.Status, resp.Header.Get("Upgrade"))
	}
	for _, line := range []string{"one\n", "two\n"} {
		io.WriteString(conn, line)
		got, err := r.ReadString('\n')
		if err != nil || got != "echo "+line {
			t.Fatalf("sent %q, got %q (%v)", line, got, err)
		}
	}
}

func TestCacheForgets(t *testing.T) {
	ann := token(t, map[string]any{"iss": testIssuer, "aud": testAudience, "exp": time.Now().Add(2 * time.Minute).Unix()})
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	s, front := newGateway(t, a)
	now := time.Now()
	s.now = func() time.Time { return now }

	get(t, front.URL+"/api", ann)
	now = now.Add(time.Minute)
	get(t, front.URL+"/api", ann)
	if n := a.reviews.Load(); n != 1 {
		t.Fatalf("%d SelfSubjectReviews within the token's life, want 1", n)
	}
	// Past its expiry the token isn't let through on the strength of the
	// earlier answer.
	now = now.Add(2 * time.Minute)
	if code, _ := get(t, front.URL+"/api", ann); code != http.StatusUnauthorized {
		t.Errorf("an expired token: got %d, want 401", code)
	}
}

// What the controller serves itself (the apps API) sits behind the same
// check: nothing gets to it without a login the API server accepts, and
// the API server never sees those requests.
func TestMounts(t *testing.T) {
	ann := login(t, "ann")
	a := newAPIServer(t, map[string]string{ann: "authelia:ann"})
	s, front := newGateway(t, a)
	var served atomic.Int32
	s.Mounts = map[string]http.Handler{"/api/v1/apps/": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		io.WriteString(w, "apps api: "+r.URL.Path)
	})}

	if code, body := get(t, front.URL+"/api/v1/apps/shop/logs", ann); code != http.StatusOK || body != "apps api: /api/v1/apps/shop/logs" {
		t.Errorf("a mounted path: %d %q", code, body)
	}
	if a.seen.Load() != nil {
		t.Error("a mounted path's request reached the API server")
	}
	for name, tok := range map[string]string{"no token": "", "not a login": "sha256~abcdef", "forged": login(t, "mallory")} {
		if code, _ := get(t, front.URL+"/api/v1/apps/shop/logs", tok); code != http.StatusUnauthorized {
			t.Errorf("%s on a mounted path: got %d, want 401", name, code)
		}
	}
	if n := served.Load(); n != 1 {
		t.Errorf("the mounted handler ran %d times, want 1", n)
	}
	// Everything else still goes to the API server.
	if code, body := get(t, front.URL+"/api/v1/pods", ann); code != http.StatusOK || !strings.HasPrefix(body, "GET /api/v1/pods") {
		t.Errorf("an API path: %d %q", code, body)
	}
}

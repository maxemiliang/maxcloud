// Package gateway lets people reach the cluster's API server from anywhere,
// without their address being on its allowlist: a reverse proxy that passes
// a request on only when it carries a login the API server accepts.
//
// It adds nothing to a request. The caller's own token goes to the API
// server unchanged, so authentication, RBAC, admission and the audit log
// work exactly as on a direct connection, and the controller's own rights
// are never lent to anyone. What it takes away is the API server's
// exposure: a request without a person's valid login is answered here.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var requests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "maxcloud_gateway_requests_total",
	Help: "Requests to the API gateway by outcome: proxied (to the API server), served (by the controller itself: the apps API), unauthenticated (no usable login), throttled (too many unknown tokens at once) or unavailable (the API server couldn't be asked).",
}, []string{"outcome"})

func init() { metrics.Registry.MustRegister(requests) }

// Server is the gateway. It runs on every replica.
type Server struct {
	// Addr to listen on, e.g. :8083.
	Addr string
	// Upstream is the API server as the controller reaches it, e.g.
	// https://kubernetes.default.svc.
	Upstream *url.URL
	// Transport reaches Upstream: its CA, and no credentials of its own.
	Transport http.RoundTripper
	// Issuer and Audience are the login provider's, as in the API server's
	// authentication configuration. A token for another issuer or audience
	// (a ServiceAccount's, say) is refused without asking the API server.
	Issuer, Audience string
	// UserPrefix is what the API server puts before a person's username
	// ("authelia:"). Whoever a token turns out to be must have it.
	UserPrefix string
	// Verifications limits how often the API server is asked about a token
	// the gateway hasn't seen (default 5 a second, 20 at once), so a flood
	// of made-up tokens stops here.
	Verifications *rate.Limiter
	// Mounts are paths the controller answers itself instead of the API
	// server (the apps API): each key is a path prefix ending in /, none of
	// them a path the API server has. A request gets to one only with a
	// login that passed the same check as any other.
	Mounts map[string]http.Handler

	once  sync.Once
	proxy *httputil.ReverseProxy
	cache tokenCache
	now   func() time.Time
}

// NeedLeaderElection: every replica serves.
func (s *Server) NeedLeaderElection() bool { return false }

// Start serves until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		// No read or write timeout: watches, log streams, exec and
		// port-forwards last as long as their caller wants.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.FromContext(ctx).WithName("gateway").Info("serving the API gateway", "addr", s.Addr, "upstream", s.Upstream.String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		// Streams never end by themselves: close what's left after a moment.
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return srv.Close()
		}
		return nil
	}
}

func (s *Server) init() {
	s.once.Do(func() {
		if s.now == nil {
			s.now = time.Now
		}
		if s.Verifications == nil {
			s.Verifications = rate.NewLimiter(5, 20)
		}
		s.proxy = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(s.Upstream)
				r.Out.Host = s.Upstream.Host
				// The API server takes these as the caller's identity only
				// from its own front proxy; nobody else has any business
				// sending them.
				for name := range r.Out.Header {
					if strings.HasPrefix(name, "X-Remote-") {
						r.Out.Header.Del(name)
					}
				}
				// Keep the caller's address as the ingress recorded it: the
				// API server's audit log reads it from here.
				r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
				r.SetXForwarded()
			},
			Transport: s.Transport,
			// Watches and log streams: every event as it comes.
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				if r.Context().Err() == nil {
					log.FromContext(r.Context()).WithName("gateway").Error(err, "proxying to the API server", "path", r.URL.Path)
				}
				requests.WithLabelValues("unavailable").Inc()
				writeStatus(w, http.StatusBadGateway, "ServiceUnavailable", "the API server can't be reached through the gateway right now")
			},
		}
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.init()
	token := bearer(r)
	if token == "" {
		requests.WithLabelValues("unauthenticated").Inc()
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "the gateway needs your cluster login: mcloud login")
		return
	}
	switch err := s.check(r.Context(), token); {
	case err == nil:
	case errors.Is(err, errThrottled):
		requests.WithLabelValues("throttled").Inc()
		w.Header().Set("Retry-After", "1")
		writeStatus(w, http.StatusTooManyRequests, "TooManyRequests", "the gateway is checking too many new logins at once: try again in a moment")
		return
	case errors.Is(err, errUnavailable):
		requests.WithLabelValues("unavailable").Inc()
		writeStatus(w, http.StatusBadGateway, "ServiceUnavailable", "the gateway can't ask the API server about your login right now")
		return
	default:
		requests.WithLabelValues("unauthenticated").Inc()
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "the gateway doesn't take this login ("+err.Error()+"): mcloud login")
		return
	}
	for prefix, h := range s.Mounts {
		if strings.HasPrefix(r.URL.Path, prefix) {
			requests.WithLabelValues("served").Inc()
			h.ServeHTTP(w, r)
			return
		}
	}
	requests.WithLabelValues("proxied").Inc()
	s.proxy.ServeHTTP(w, r)
}

var (
	errThrottled   = errors.New("too many unknown tokens")
	errUnavailable = errors.New("the API server can't be asked")
)

// check says whether token is a person's login the API server accepts.
// Unknown tokens are first weeded out by what they claim (no signature
// check, so no keys to fetch): only one that claims to be this provider's,
// for this cluster and not expired, is put to the API server, which is the
// one that verifies it. Answers are remembered: yes until the token
// expires (at most 5 minutes; the API server checks every request anyway),
// no for 30 seconds.
func (s *Server) check(ctx context.Context, token string) error {
	now := s.now()
	key := sha256.Sum256([]byte(token))
	if err, ok := s.cache.get(key, now); ok {
		return err
	}
	exp, err := s.claims(token, now)
	if err != nil {
		return err // cheap to work out again: not worth remembering
	}
	if !s.Verifications.Allow() {
		return errThrottled
	}
	user, err := s.whoami(ctx, token)
	switch {
	case errors.Is(err, errUnavailable):
		return err
	case err == nil && !strings.HasPrefix(user, s.UserPrefix):
		err = errors.New("it isn't a person's login")
	}
	until := now.Add(30 * time.Second)
	if err == nil {
		if until = now.Add(5 * time.Minute); exp.Before(until) {
			until = exp
		}
	}
	s.cache.put(key, err, until, now)
	return err
}

// claims reads what a token says about itself, unverified, and refuses one
// that can't be this cluster's login. It returns when the token expires.
func (s *Server) claims(token string, now time.Time) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16<<10 {
		return time.Time{}, errors.New("not an ID token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("not an ID token")
	}
	var c struct {
		Iss string   `json:"iss"`
		Aud audience `json:"aud"`
		Exp float64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, errors.New("not an ID token")
	}
	if c.Iss != s.Issuer || !slices.Contains(c.Aud, s.Audience) {
		return time.Time{}, errors.New("it isn't from this cluster's login provider")
	}
	exp := time.Unix(int64(c.Exp), 0)
	if !exp.After(now) {
		return time.Time{}, errors.New("it expired")
	}
	return exp, nil
}

// audience is a JWT's aud: one string, or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// whoami asks the API server who the token's bearer is, as that bearer: a
// SelfSubjectReview, which every authenticated user may create. So the
// answer comes from the same authentication a proxied request goes through,
// and the controller needs no right to review tokens.
func (s *Server) whoami(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := s.Upstream.JoinPath("/apis/authentication.k8s.io/v1/selfsubjectreviews")
	body := `{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.Transport.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", errors.New("the cluster doesn't accept it")
	case resp.StatusCode/100 != 2:
		return "", fmt.Errorf("%w: selfsubjectreviews: %s", errUnavailable, resp.Status)
	}
	var review struct {
		Status struct {
			UserInfo struct {
				Username string `json:"username"`
			} `json:"userInfo"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return "", fmt.Errorf("%w: selfsubjectreviews: %v", errUnavailable, err)
	}
	if review.Status.UserInfo.Username == "" {
		return "", errors.New("the cluster doesn't accept it")
	}
	return review.Status.UserInfo.Username, nil
}

func bearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// writeStatus answers the way the API server does, so kubectl and client-go
// show the message.
func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{},
		"status": "Failure", "message": message, "reason": reason, "code": code,
	})
}

// tokenCache remembers what check found, by the token's hash.
type tokenCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]cached
}

type cached struct {
	err   error
	until time.Time
}

// maxCached bounds the cache; the verification limit already keeps it far
// below this.
const maxCached = 10000

func (c *tokenCache) get(key [sha256.Size]byte, now time.Time) (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.until) {
		return nil, false
	}
	return e.err, true
}

func (c *tokenCache) put(key [sha256.Size]byte, err error, until, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[sha256.Size]byte]cached{}
	}
	if len(c.entries) >= maxCached {
		for k, e := range c.entries {
			if !now.Before(e.until) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCached {
			clear(c.entries)
		}
	}
	c.entries[key] = cached{err: err, until: until}
}

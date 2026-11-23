// Package appapi lets an app's owners read what the platform knows about
// their app and nothing else: its logs from Loki, its request and queue
// metrics from Prometheus, and the platform's alerts about it. It also
// lets them change the app's secrets, which are kept in OpenBao
// (secrets.go). Loki and
// Prometheus aren't multi-tenant, and app pods can't reach them, so the
// controller asks them on the caller's behalf, with the app's namespace
// fixed by the server:
//
//  1. the caller's bearer token (an mcloud login's ID token) must be one
//     the API server accepts (a TokenReview, package caller);
//  2. that user must have a permission in the app's namespace that only
//     its owners and admins have (a SubjectAccessReview): get pods/log for
//     logs, list pods for metrics and alerts, get (to list) or update (to
//     change) the Secret app-secrets for secrets;
//  3. the namespace must be an app's (run.gronblom.dev/tenant);
//  4. every query is built here, with namespace="<app>" in each selector.
//     The client picks a time range and filters within the app, never the
//     query.
//
// Routes, JSON:
//
//	GET    /api/v1/apps/{app}/logs     ?since=1h&until=...&limit=1000&pod=<regex>&container=<name>&all=true
//	GET    /api/v1/apps/{app}/metrics  ?window=1h
//	GET    /api/v1/apps/{app}/alerts
//	GET    /api/v1/apps/{app}/secrets
//	PUT    /api/v1/apps/{app}/secrets/{name}
//	DELETE /api/v1/apps/{app}/secrets/{name}
package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
)

// Prefix is where the routes are mounted.
const Prefix = "/api/v1/apps/"

// TenantLabel marks an app's namespace (the app registry sets it).
const TenantLabel = "run.gronblom.dev/tenant"

// Features the handler serves, for the discovery document.
const (
	FeatureLogs    = "logs"
	FeatureMetrics = "metrics"
	FeatureAlerts  = "alerts"
)

// Handler serves the routes. Logs need Loki; metrics and alerts need
// Prometheus; secrets need OpenBao. A route whose backend isn't
// configured answers 404.
type Handler struct {
	Reviewer *caller.Reviewer
	// Reader reads namespaces and PrometheusRules (uncached: the manager's
	// APIReader).
	Reader client.Reader
	// Loki and Prometheus are base URLs, e.g.
	// http://loki.monitoring.svc.cluster.local:3100. Empty turns the
	// routes that need them off.
	Loki, Prometheus string
	// RuleNamespaces: only alerts from PrometheusRules in these namespaces
	// are shown (default: monitoring). An app's own rules can query any
	// app's metrics, so their alerts could carry another app's data.
	RuleNamespaces []string
	// HTTP talks to Loki and Prometheus (default: 30s timeout).
	HTTP *http.Client
	// MaxConcurrent queries to Loki and Prometheus at once (default 4);
	// more wait for a slot.
	MaxConcurrent int
	// Secrets is OpenBao's KV, where apps' secrets are (secrets.go). Nil
	// turns the secrets routes off.
	Secrets SecretStore
	// Writer annotates ExternalSecrets, so ESO syncs a change right away.
	Writer client.Writer
	// SyncTimeout is how long a change waits for the Secret to have it
	// (default 30s); SyncPoll how often it looks (default 500ms).
	SyncTimeout, SyncPoll time.Duration

	mux   *http.ServeMux
	slots chan struct{}
	rules *trustedRules
}

// Features lists what this handler serves.
func (h *Handler) Features() []string {
	var f []string
	if h.Loki != "" {
		f = append(f, FeatureLogs)
	}
	if h.Prometheus != "" {
		f = append(f, FeatureMetrics, FeatureAlerts)
	}
	if h.Secrets != nil {
		f = append(f, FeatureSecrets)
	}
	return f
}

func (h *Handler) init() {
	if h.mux != nil {
		return
	}
	n := h.MaxConcurrent
	if n <= 0 {
		n = 4
	}
	h.slots = make(chan struct{}, n)
	if h.HTTP == nil {
		h.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	nss := h.RuleNamespaces
	if len(nss) == 0 {
		nss = []string{"monitoring"}
	}
	h.rules = &trustedRules{reader: h.Reader, namespaces: nss}
	h.mux = http.NewServeMux()
	if h.Loki != "" {
		h.mux.HandleFunc("GET "+Prefix+"{app}/logs", h.logs)
	}
	if h.Prometheus != "" {
		h.mux.HandleFunc("GET "+Prefix+"{app}/metrics", h.metrics)
		h.mux.HandleFunc("GET "+Prefix+"{app}/alerts", h.alerts)
	}
	if h.Secrets != nil {
		h.mux.HandleFunc("GET "+Prefix+"{app}/secrets", h.listSecrets)
		h.mux.HandleFunc("PUT "+Prefix+"{app}/secrets/{name}", h.putSecret)
		h.mux.HandleFunc("DELETE "+Prefix+"{app}/secrets/{name}", h.deleteSecret)
	}
}

// Init prepares the handler; ServeHTTP calls it too. Call it before
// serving from several goroutines.
func (h *Handler) Init() { h.init() }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

// appNameRe is a DNS label, as app names and namespaces are.
var appNameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// httpError is an error with the status to answer.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errorf(status int, format string, a ...any) error {
	return &httpError{status: status, msg: fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	if errors.As(err, &he) {
		if he.status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="maxcloud"`)
		}
		writeJSON(w, he.status, map[string]string{"error": he.msg})
		return
	}
	if r.Context().Err() != nil {
		return // the client went away
	}
	log.FromContext(r.Context()).WithName("appapi").Error(err, "request failed", "path", r.URL.Path)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

// authorize checks the caller and returns the app (its namespace).
// p's Namespace is set to the app's.
func (h *Handler) authorize(r *http.Request, p caller.Permission) (string, error) {
	app := r.PathValue("app")
	if len(app) > 63 || !appNameRe.MatchString(app) {
		return "", errorf(http.StatusBadRequest, "%q isn't an app name", app)
	}
	ctx := r.Context()
	u, err := h.Reviewer.Authenticate(ctx, caller.BearerToken(r))
	if errors.Is(err, caller.ErrUnauthenticated) {
		return "", errorf(http.StatusUnauthorized, "%v", err)
	}
	if err != nil {
		return "", err
	}
	p.Namespace = app
	ok, err := h.Reviewer.Allowed(ctx, u, p)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errorf(http.StatusForbidden, "%s may not %s: only the app's owners and admins may", u.Name, p)
	}
	var ns corev1.Namespace
	if err := h.Reader.Get(ctx, types.NamespacedName{Name: app}, &ns); apierrors.IsNotFound(err) {
		return "", errorf(http.StatusNotFound, "no app %s", app)
	} else if err != nil {
		return "", err
	}
	if ns.Labels[TenantLabel] != "true" {
		return "", errorf(http.StatusNotFound, "%s isn't an app's namespace", app)
	}
	return app, nil
}

// slot waits for a free backend slot; release it with the returned func.
func (h *Handler) slot(ctx context.Context) (func(), error) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// getJSON GETs base+path with query and decodes the answer.
func (h *Handler) getJSON(ctx context.Context, base, path string, q map[string]string, out any) error {
	release, err := h.slot(ctx)
	if err != nil {
		return err
	}
	defer release()
	vals := make([]string, 0, len(q))
	for k, v := range q {
		vals = append(vals, k+"="+urlQueryEscape(v))
	}
	u := strings.TrimRight(base, "/") + path
	if len(vals) > 0 {
		u += "?" + strings.Join(vals, "&")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("%s: %s: %s", path, resp.Status, e.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// parseTime reads a duration ("90m", "2h", "7d") or an RFC 3339 time, as
// a point in the past.
func parseTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d, err := parseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("%q: a duration like 90m, 2h or 7d, or an RFC 3339 time", s)
	}
	return now.Add(-d), nil
}

// parseDuration is time.ParseDuration plus days ("7d").
func parseDuration(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

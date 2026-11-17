package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
)

const (
	trustedUID = "11111111-2222-3333-4444-555555555555"
	appRuleUID = "99999999-8888-7777-6666-555555555555"
)

// backend records the queries Loki and Prometheus got.
type backend struct {
	mu      sync.Mutex
	queries []string
}

func (b *backend) record(q string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, q)
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.URL.Path {
	case "/loki/api/v1/query_range":
		b.record(q.Get("query"))
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{
			map[string]any{"stream": map[string]string{"namespace": "shop", "pod": "web-00001-deployment-a-b", "container": "user-container"},
				"values": [][2]string{{"1763380800000000002", "second"}, {"1763380800000000001", "first"}}},
		}}})
	case "/api/v1/query":
		b.record(q.Get("query"))
		var result []any
		switch {
		case strings.Contains(q.Get("query"), "histogram_quantile"):
			result = []any{map[string]any{"metric": map[string]string{"kn_service_name": "web"}, "value": []any{1.0, "0.25"}}}
		case strings.Contains(q.Get("query"), "5.."):
			result = []any{map[string]any{"metric": map[string]string{"kn_service_name": "web"}, "value": []any{1.0, "3.2"}}}
		case strings.Contains(q.Get("query"), "increase("):
			result = []any{
				map[string]any{"metric": map[string]string{"kn_service_name": "web"}, "value": []any{1.0, "360.4"}},
				map[string]any{"metric": map[string]string{"kn_service_name": "idle"}, "value": []any{1.0, "0"}},
			}
		case strings.Contains(q.Get("query"), "keda_scaler_metrics_value"):
			result = []any{map[string]any{"metric": map[string]string{"scaledObject": "mail", "type": "ScaledJob"}, "value": []any{1.0, "7"}}}
		case strings.Contains(q.Get("query"), "kube_job_status_active"):
			result = []any{map[string]any{"metric": map[string]string{}, "value": []any{1.0, "2"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	case "/api/v1/rules":
		alert := func(name string, labels map[string]string) map[string]any {
			return map[string]any{"name": name, "alerts": []any{map[string]any{"labels": labels, "annotations": map[string]string{"summary": name + " summary"}, "state": "firing", "activeAt": "2026-11-17T10:00:00Z", "value": "0.0833333"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"groups": []any{
			map[string]any{"name": "homelab.knative", "file": "/etc/prometheus/rules/prometheus-rulefiles-0/monitoring-knative-" + trustedUID + ".yaml", "rules": []any{
				alert("KnativeServiceErrors", map[string]string{"alertname": "KnativeServiceErrors", "namespace": "shop", "kn_service_name": "web", "severity": "warning"}),
				alert("KnativeServiceErrors", map[string]string{"alertname": "KnativeServiceErrors", "namespace": "other", "kn_service_name": "web"}),
				alert("MaxcloudDatabaseBlocked", map[string]string{"alertname": "MaxcloudDatabaseBlocked", "namespace": "maxcloud-system", "app": "shop"}),
			}},
			// An app's own PrometheusRule: its query could read anyone's
			// metrics, so its alerts aren't shown, even labelled shop.
			map[string]any{"name": "mine", "file": "/etc/prometheus/rules/prometheus-rulefiles-0/shop-mine-" + appRuleUID + ".yaml", "rules": []any{
				alert("Exfiltrate", map[string]string{"alertname": "Exfiltrate", "namespace": "shop"}),
			}},
		}}})
	default:
		http.NotFound(w, r)
	}
}

func newHandler(t *testing.T) (*Handler, *backend) {
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
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(ns("shop", true), ns("other", true), ns("kube-system", false)).Build()
	rules := map[string][]types.UID{"monitoring": {trustedUID}, "shop": {appRuleUID}}
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authnv1.TokenReview:
				users := map[string]string{"ann-token": "authelia:ann", "admin-token": "authelia:root"}
				if u, ok := users[o.Spec.Token]; ok {
					o.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: u}}
				}
				return nil
			case *authzv1.SubjectAccessReview:
				ra := o.Spec.ResourceAttributes
				// ann owns shop; root is an admin.
				o.Status.Allowed = o.Spec.User == "authelia:root" || o.Spec.User == "authelia:ann" && ra.Namespace == "shop"
				return nil
			}
			return errors.New("unexpected create")
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			l, ok := list.(*unstructured.UnstructuredList)
			if !ok {
				return c.List(ctx, list, opts...)
			}
			var lo client.ListOptions
			lo.ApplyOptions(opts)
			for _, uid := range rules[lo.Namespace] {
				var it unstructured.Unstructured
				it.SetUID(uid)
				l.Items = append(l.Items, it)
			}
			return nil
		},
	})
	b := &backend{}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return &Handler{Reviewer: &caller.Reviewer{Client: c}, Reader: c, Loki: srv.URL, Prometheus: srv.URL}, b
}

func get(h http.Handler, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAccess(t *testing.T) {
	h, b := newHandler(t)
	for _, c := range []struct {
		token, path string
		want        int
	}{
		{"", "/api/v1/apps/shop/logs", http.StatusUnauthorized},
		{"wrong", "/api/v1/apps/shop/metrics", http.StatusUnauthorized},
		{"ann-token", "/api/v1/apps/other/logs", http.StatusForbidden},
		{"ann-token", "/api/v1/apps/other/alerts", http.StatusForbidden},
		{"ann-token", "/api/v1/apps/Shop/logs", http.StatusBadRequest},
		{"ann-token", "/api/v1/apps/shop/logs?pod=(", http.StatusBadRequest},
		{"ann-token", "/api/v1/apps/shop/logs?since=40d", http.StatusBadRequest},
		{"ann-token", "/api/v1/apps/shop/metrics?window=1s", http.StatusBadRequest},
		{"admin-token", "/api/v1/apps/kube-system/logs", http.StatusNotFound}, // not an app
		{"admin-token", "/api/v1/apps/gone/logs", http.StatusNotFound},
		{"ann-token", "/api/v1/apps/shop/nothing", http.StatusNotFound},
		{"ann-token", "/api/v1/apps/shop/logs", http.StatusOK},
		{"admin-token", "/api/v1/apps/other/metrics", http.StatusOK},
	} {
		w := get(h, c.token, c.path)
		if w.Code != c.want {
			t.Errorf("%s as %q: %d %s, want %d", c.path, c.token, w.Code, w.Body, c.want)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", c.path, w.Header().Get("Cache-Control"))
		}
	}
	// Refused requests never reached Loki or Prometheus; the queries that
	// did are about the app in the path only.
	for _, q := range b.queries {
		if !regexp.MustCompile(`namespace(_name)?="(shop|other)"`).MatchString(q) {
			t.Errorf("query without the app's namespace: %s", q)
		}
	}
}

func TestLogs(t *testing.T) {
	h, b := newHandler(t)
	w := get(h, "ann-token", "/api/v1/apps/shop/logs?since=2h&limit=2&pod="+url.QueryEscape("web-[0-9]+-deployment-.+")+`&container=user-container`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp LogsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Entries) != 2 || resp.Entries[0].Line != "first" || resp.Entries[1].Line != "second" || !resp.Limited {
		t.Errorf("entries %+v, limited %v", resp.Entries, resp.Limited)
	}
	if d := resp.End.Sub(resp.Start); d != 2*time.Hour {
		t.Errorf("range %v", d)
	}
	want := `{namespace="shop", pod=~"web-[0-9]+-deployment-.+", container="user-container"}`
	if got := b.queries[len(b.queries)-1]; got != want {
		t.Errorf("query %s", got)
	}
	// A pod filter can't step outside the namespace: it's quoted into the
	// one selector.
	if got := logQuery("shop", `x"} or {namespace="other`, "", false); got != `{namespace="shop", pod=~"x\"} or {namespace=\"other", container!="queue-proxy"}` {
		t.Errorf("escaping: %s", got)
	}
}

func TestMetrics(t *testing.T) {
	h, _ := newHandler(t)
	w := get(h, "ann-token", "/api/v1/apps/shop/metrics?window=1h")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var m MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Window != "1h" || len(m.Services) != 2 {
		t.Fatalf("%+v", m)
	}
	idle, web := m.Services[0], m.Services[1]
	if web.Name != "web" || web.Requests != 360 || web.Errors != 3 || web.ErrorRatio == nil || *web.P95Seconds != 0.25 {
		t.Errorf("web %+v", web)
	}
	if idle.Name != "idle" || idle.ErrorRatio != nil || idle.P95Seconds != nil {
		t.Errorf("idle %+v", idle)
	}
	if len(m.Queues) != 1 || m.Queues[0].Name != "mail" || m.Queues[0].Kind != "scaledjob" || m.Queues[0].Backlog != 7 || m.Jobs.Running != 2 {
		t.Errorf("queues %+v, jobs %+v", m.Queues, m.Jobs)
	}
}

func TestAlerts(t *testing.T) {
	h, _ := newHandler(t)
	w := get(h, "ann-token", "/api/v1/apps/shop/alerts")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var a AlertsResponse
	json.Unmarshal(w.Body.Bytes(), &a)
	var names []string
	for _, al := range a.Alerts {
		names = append(names, al.Name)
	}
	if strings.Join(names, " ") != "KnativeServiceErrors MaxcloudDatabaseBlocked" {
		t.Errorf("alerts %v", names)
	}
	if a.Alerts[0].Value != "0.08333" || a.Alerts[0].Summary != "KnativeServiceErrors summary" {
		t.Errorf("%+v", a.Alerts[0])
	}
}

func TestRuleFileUID(t *testing.T) {
	for in, want := range map[string]string{
		"/etc/prometheus/rules/x-rulefiles-0/monitoring-knative-" + trustedUID + ".yaml": trustedUID,
		"monitoring-x.yaml": "",
		"":                  "",
	} {
		if got := ruleFileUID(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

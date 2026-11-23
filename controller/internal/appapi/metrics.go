package appapi

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
)

// The window metrics are summed over: from 5 minutes to the 15 days
// Prometheus keeps.
const (
	minWindow     = 5 * time.Minute
	maxWindow     = 15 * 24 * time.Hour
	defaultWindow = time.Hour
)

// ServiceMetrics are one Knative service's requests over the window, as
// its queue-proxy saw them (the "Run platform" dashboard's numbers).
type ServiceMetrics struct {
	Name              string   `json:"name"`
	Requests          float64  `json:"requests"`
	RequestsPerSecond float64  `json:"requestsPerSecond"`
	Errors            float64  `json:"errors"`               // 5xx answers
	ErrorRatio        *float64 `json:"errorRatio"`           // errors/requests; null without requests
	P95Seconds        *float64 `json:"p95Seconds"`           // null without requests
	Pods              *float64 `json:"pods"`                 // ready pods now, from Knative's autoscaler
	PodsWanted        *float64 `json:"podsWanted,omitempty"` // what the autoscaler asks for
}

// QueueMetrics is a KEDA ScaledJob's or ScaledObject's trigger: the jobs
// waiting in its queue, as KEDA last read them.
type QueueMetrics struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"` // scaledjob or scaledobject
	Backlog float64 `json:"backlog"`
}

// JobMetrics are the app's Kubernetes Jobs (scheduled and queue functions).
type JobMetrics struct {
	Running float64 `json:"running"`
	Failed  float64 `json:"failed"` // Jobs that exist now and failed
}

// MetricsResponse is everything /metrics returns.
type MetricsResponse struct {
	App      string           `json:"app"`
	Window   string           `json:"window"`
	At       time.Time        `json:"at"`
	Services []ServiceMetrics `json:"services"`
	Queues   []QueueMetrics   `json:"queues"`
	Jobs     JobMetrics       `json:"jobs"`
}

// metricQueries are the PromQL for an app, each with namespace fixed to it.
// app is a DNS label (checked), so it needs no escaping beyond quotes.
func metricQueries(app string, window time.Duration) map[string]string {
	ns := strconv.Quote(app)
	w := promDuration(window)
	req := `http_server_request_duration_seconds_count{container="queue-proxy", namespace=` + ns
	return map[string]string{
		"requests": `sum by (kn_service_name) (increase(` + req + `}[` + w + `]))`,
		"errors":   `sum by (kn_service_name) (increase(` + req + `, http_response_status_code=~"5.."}[` + w + `]))`,
		"p95": `histogram_quantile(0.95, sum by (kn_service_name, le) (rate(http_server_request_duration_seconds_bucket{container="queue-proxy", namespace=` +
			ns + `}[` + w + `])))`,
		"pods":       `sum by (kn_service_name) (kn_revision_pods_count{k8s_namespace_name=` + ns + `})`,
		"podsWanted": `sum by (kn_service_name) (kn_revision_pods_desired{k8s_namespace_name=` + ns + `})`,
		// KEDA's operator (PodMonitor monitoring/keda) exports the
		// ScaledObject's namespace, which Prometheus renames to
		// exported_namespace next to the operator's own (keda), unless the
		// monitor honours labels. Only KEDA's own series count, so an app's
		// metrics can't pose as another app's queue.
		"backlog": `max by (scaledObject, type) (keda_scaler_metrics_value{job="monitoring/keda", exported_namespace=` + ns + `})` +
			` or max by (scaledObject, type) (keda_scaler_metrics_value{job="monitoring/keda", namespace=` + ns + `})`,
		"jobsRunning": `sum(kube_job_status_active{namespace=` + ns + `})`,
		"jobsFailed":  `count(kube_job_status_failed{namespace=` + ns + `} > 0)`,
	}
}

// promDuration writes d the way PromQL reads it.
func promDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	if d%time.Minute == 0 {
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Second)) + "s"
}

type sample struct {
	labels map[string]string
	value  float64
}

// query runs an instant query and returns its vector.
func (h *Handler) query(ctx context.Context, q string, at time.Time) ([]sample, error) {
	var out struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	err := h.getJSON(ctx, h.Prometheus, "/api/v1/query", map[string]string{
		"query": q, "time": strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64),
	}, &out)
	if err != nil {
		return nil, err
	}
	var s []sample
	for _, r := range out.Data.Result {
		str, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			continue
		}
		s = append(s, sample{labels: r.Metric, value: v})
	}
	return s, nil
}

func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, caller.Permission{Verb: "list", Resource: "pods"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	window := defaultWindow
	if s := r.URL.Query().Get("window"); s != "" {
		if window, err = parseDuration(s); err != nil || window < minWindow || window > maxWindow {
			h.fail(w, r, errorf(http.StatusBadRequest, "window: a duration from 5m to 15d, like 1h or 7d"))
			return
		}
		window = window.Round(time.Second)
	}
	at := time.Now()
	queries := metricQueries(app, window)
	results := map[string][]sample{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for name, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := h.query(r.Context(), q, at)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			results[name] = s
		}()
	}
	wg.Wait()
	if firstErr != nil {
		h.fail(w, r, firstErr)
		return
	}
	writeJSON(w, http.StatusOK, buildMetrics(app, window, at, results))
}

// buildMetrics puts the queries' answers together.
func buildMetrics(app string, window time.Duration, at time.Time, res map[string][]sample) MetricsResponse {
	out := MetricsResponse{App: app, Window: promDuration(window), At: at.UTC(), Services: []ServiceMetrics{}, Queues: []QueueMetrics{}}
	svcs := map[string]*ServiceMetrics{}
	svc := func(s sample) *ServiceMetrics {
		name := s.labels["kn_service_name"]
		if svcs[name] == nil {
			svcs[name] = &ServiceMetrics{Name: name}
		}
		return svcs[name]
	}
	ptr := func(v float64) *float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return &v
	}
	for _, s := range res["requests"] {
		m := svc(s)
		m.Requests = round(s.value)
		m.RequestsPerSecond = s.value / window.Seconds()
	}
	for _, s := range res["errors"] {
		svc(s).Errors = round(s.value)
	}
	for _, s := range res["p95"] {
		svc(s).P95Seconds = ptr(s.value)
	}
	for _, s := range res["pods"] {
		svc(s).Pods = ptr(s.value)
	}
	for _, s := range res["podsWanted"] {
		svc(s).PodsWanted = ptr(s.value)
	}
	for _, m := range svcs {
		if m.Name == "" {
			continue
		}
		if m.Requests > 0 {
			m.ErrorRatio = ptr(m.Errors / m.Requests)
		} else {
			m.P95Seconds = nil
		}
		out.Services = append(out.Services, *m)
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	for _, s := range res["backlog"] {
		out.Queues = append(out.Queues, QueueMetrics{Name: s.labels["scaledObject"], Kind: strings.ToLower(s.labels["type"]), Backlog: s.value})
	}
	sort.Slice(out.Queues, func(i, j int) bool { return out.Queues[i].Name < out.Queues[j].Name })
	if s := res["jobsRunning"]; len(s) > 0 {
		out.Jobs.Running = s[0].value
	}
	if s := res["jobsFailed"]; len(s) > 0 {
		out.Jobs.Failed = s[0].value
	}
	return out
}

// round: increase() extrapolates, so counts come out fractional.
func round(v float64) float64 { return math.Round(v) }

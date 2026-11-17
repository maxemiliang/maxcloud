package appapi

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list

// Alert is one of the platform's alerts about the app, pending (its
// condition holds, not yet for long enough) or firing.
type Alert struct {
	Name        string            `json:"name"`
	State       string            `json:"state"` // pending or firing
	Severity    string            `json:"severity,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Description string            `json:"description,omitempty"`
	ActiveAt    *time.Time        `json:"activeAt,omitempty"`
	Value       string            `json:"value,omitempty"`
	Labels      map[string]string `json:"labels"`
}

// AlertsResponse is everything /alerts returns.
type AlertsResponse struct {
	App    string  `json:"app"`
	Alerts []Alert `json:"alerts"`
}

// belongsTo says whether an alert's labels are about app: the namespace of
// what it's about (kube-state-metrics, queue-proxy, CloudNativePG),
// Knative's own label for it, or the app label of the maxcloud controller's
// metrics (MaxcloudDatabaseBlocked).
func belongsTo(labels map[string]string, app string) bool {
	switch {
	case labels["namespace"] == app, labels["k8s_namespace_name"] == app:
		return true
	case labels["namespace"] == "maxcloud-system" && labels["app"] == app:
		return true
	case labels["namespace"] == "keda" && labels["exported_namespace"] == app:
		return true
	}
	return false
}

func (h *Handler) alerts(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, "list", "pods", "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	trusted, err := h.rules.uids(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var out struct {
		Data struct {
			Groups []ruleGroup `json:"groups"`
		} `json:"data"`
	}
	if err := h.getJSON(r.Context(), h.Prometheus, "/api/v1/rules", map[string]string{"type": "alert"}, &out); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, AlertsResponse{App: app, Alerts: appAlerts(out.Data.Groups, trusted, app)})
}

type ruleGroup struct {
	Name  string `json:"name"`
	File  string `json:"file"`
	Rules []struct {
		Name   string `json:"name"`
		Alerts []struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			State       string            `json:"state"`
			ActiveAt    *time.Time        `json:"activeAt"`
			Value       string            `json:"value"`
		} `json:"alerts"`
	} `json:"rules"`
}

// appAlerts picks the active alerts about app from rule groups whose file
// is a trusted PrometheusRule's.
func appAlerts(groups []ruleGroup, trusted map[string]bool, app string) []Alert {
	out := []Alert{}
	for _, g := range groups {
		if !trusted[ruleFileUID(g.File)] {
			continue
		}
		for _, rule := range g.Rules {
			for _, a := range rule.Alerts {
				if a.State != "firing" && a.State != "pending" || !belongsTo(a.Labels, app) {
					continue
				}
				out = append(out, Alert{
					Name: rule.Name, State: a.State, Severity: a.Labels["severity"],
					Summary: a.Annotations["summary"], Description: a.Annotations["description"],
					ActiveAt: a.ActiveAt, Value: formatValue(a.Value), Labels: a.Labels,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State == "firing"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ruleFileUID is the PrometheusRule UID in a rule file's name: the
// Prometheus Operator writes each PrometheusRule to
// .../<namespace>-<name>-<uid>.yaml. UIDs are unique, unlike the
// namespace-name prefix (an app called monitoring-x would match
// "monitoring-").
func ruleFileUID(file string) string {
	base := strings.TrimSuffix(path.Base(file), ".yaml")
	if len(base) < 37 || base[len(base)-37] != '-' {
		return ""
	}
	return base[len(base)-36:]
}

var gvkRuleList = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusRuleList"}

// trustedRules are the UIDs of the PrometheusRules in the platform's
// namespaces, read every few minutes.
type trustedRules struct {
	reader     client.Reader
	namespaces []string

	mu   sync.Mutex
	set  map[string]bool
	read time.Time
}

func (t *trustedRules) uids(ctx context.Context) (map[string]bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.set != nil && time.Since(t.read) < 5*time.Minute {
		return t.set, nil
	}
	set := map[string]bool{}
	for _, ns := range t.namespaces {
		l := &unstructured.UnstructuredList{}
		l.SetGroupVersionKind(gvkRuleList)
		if err := t.reader.List(ctx, l, client.InNamespace(ns)); err != nil {
			if t.set != nil {
				return t.set, nil // the last good read
			}
			return nil, err
		}
		for _, it := range l.Items {
			set[string(it.GetUID())] = true
		}
	}
	t.set, t.read = set, time.Now()
	return set, nil
}

// formatValue keeps an alert's value short.
func formatValue(v string) string {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	return strconv.FormatFloat(f, 'g', 4, 64)
}

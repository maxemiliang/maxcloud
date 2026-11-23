// Package appapi is mcloud's client for the platform's apps API: an app's
// logs from Loki, its metrics from Prometheus, the platform's alerts
// about it, and its secrets (kept in OpenBao). The maxcloud controller serves it next to the discovery
// document (controller/internal/appapi), and answers only an app's owners
// and admins: mcloud sends the same token its kubeconfig does.
package appapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Features the API may offer (the discovery document lists them).
const (
	Logs    = "logs"
	Metrics = "metrics"
	Alerts  = "alerts"
	Secrets = "secrets"
)

// ErrForbidden: the API refused (not one of the app's owners).
var ErrForbidden = errors.New("forbidden")

// Client calls the API. Base is the apps API's URL
// (https://maxcloud-api.gronblom.dev/api/v1/apps/); HTTP adds the bearer token.
type Client struct {
	Base string
	HTTP *http.Client
}

// RequestError is the API refusing a request as asked (400, 404, 413):
// Msg says why.
type RequestError struct {
	Status string
	Msg    string
}

func (e *RequestError) Error() string { return e.Status + ": " + e.Msg }

func (c *Client) get(ctx context.Context, app, what string, q url.Values, out any) error {
	_, err := c.do(ctx, http.MethodGet, app, what, q, nil, out)
	return err
}

// do sends a request about app and decodes a 200 or 202 answer into out;
// it returns the status.
func (c *Client) do(ctx context.Context, method, app, what string, q url.Values, in, out any) (int, error) {
	u := strings.TrimRight(c.Base, "/") + "/" + url.PathEscape(app) + "/" + what
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(body))
			if len(e.Error) > 200 {
				e.Error = e.Error[:200]
			}
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return resp.StatusCode, fmt.Errorf("the platform didn't accept your login (%s): mcloud login, or a kubeconfig from it", e.Error)
		case http.StatusForbidden:
			return resp.StatusCode, fmt.Errorf("%w: %s", ErrForbidden, e.Error)
		case http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge:
			return resp.StatusCode, &RequestError{Status: resp.Status, Msg: e.Error}
		}
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return resp.StatusCode, json.Unmarshal(body, out)
}

// LogQuery picks lines: a range (Since before Until; zero Until is now),
// pods matching Pod (a regular expression, anchored), one Container or
// every one but Knative's queue-proxy (unless AllContainers), and at most
// Limit lines, the newest.
type LogQuery struct {
	Since, Until  time.Time
	Pod           string
	Container     string
	AllContainers bool
	Limit         int
}

// LogEntry is one line.
type LogEntry struct {
	Time      time.Time `json:"time"`
	Pod       string    `json:"pod"`
	Container string    `json:"container"`
	Line      string    `json:"line"`
}

// LogsResponse: the newest lines in the range, oldest first; Limited says
// there were more.
type LogsResponse struct {
	App     string     `json:"app"`
	Start   time.Time  `json:"start"`
	End     time.Time  `json:"end"`
	Entries []LogEntry `json:"entries"`
	Limited bool       `json:"limited"`
}

// Logs reads an app's logs from Loki.
func (c *Client) Logs(ctx context.Context, app string, q LogQuery) (*LogsResponse, error) {
	v := url.Values{}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	if q.Pod != "" {
		v.Set("pod", q.Pod)
	}
	if q.Container != "" {
		v.Set("container", q.Container)
	}
	if q.AllContainers {
		v.Set("all", "true")
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out LogsResponse
	return &out, c.get(ctx, app, "logs", v, &out)
}

// ServiceMetrics are one Knative service's requests over the window.
// Pointers are null when there's nothing to say (no requests, no pods
// reported).
type ServiceMetrics struct {
	Name              string   `json:"name"`
	Requests          float64  `json:"requests"`
	RequestsPerSecond float64  `json:"requestsPerSecond"`
	Errors            float64  `json:"errors"`
	ErrorRatio        *float64 `json:"errorRatio"`
	P95Seconds        *float64 `json:"p95Seconds"`
	Pods              *float64 `json:"pods"`
	PodsWanted        *float64 `json:"podsWanted,omitempty"`
}

// QueueMetrics is a queue function's backlog, as KEDA last read it.
type QueueMetrics struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Backlog float64 `json:"backlog"`
}

// JobMetrics are the app's Jobs now.
type JobMetrics struct {
	Running float64 `json:"running"`
	Failed  float64 `json:"failed"`
}

// MetricsResponse is an app's metrics over a window.
type MetricsResponse struct {
	App      string           `json:"app"`
	Window   string           `json:"window"`
	At       time.Time        `json:"at"`
	Services []ServiceMetrics `json:"services"`
	Queues   []QueueMetrics   `json:"queues"`
	Jobs     JobMetrics       `json:"jobs"`
}

// Metrics reads an app's metrics over window (e.g. 1h, 7d).
func (c *Client) Metrics(ctx context.Context, app, window string) (*MetricsResponse, error) {
	v := url.Values{}
	if window != "" {
		v.Set("window", window)
	}
	var out MetricsResponse
	return &out, c.get(ctx, app, "metrics", v, &out)
}

// Alert is one of the platform's alerts about the app.
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

// AlertsResponse lists the active alerts.
type AlertsResponse struct {
	App    string  `json:"app"`
	Alerts []Alert `json:"alerts"`
}

// Alerts reads the platform's active alerts about an app.
func (c *Client) Alerts(ctx context.Context, app string) (*AlertsResponse, error) {
	var out AlertsResponse
	return &out, c.get(ctx, app, "alerts", nil, &out)
}

// SecretsResponse lists an app's secrets, never their values. Version and
// Updated are those of the one OpenBao secret that holds them all.
type SecretsResponse struct {
	App     string       `json:"app"`
	Version int          `json:"version"`
	Updated *time.Time   `json:"updated,omitempty"`
	Secrets []SecretName `json:"secrets"`
}

// SecretName is one secret's name.
type SecretName struct {
	Name string `json:"name"`
}

// SecretChange is the answer to a change. Synced: the app's Secret
// app-secrets has it already; if not, Message says why (the platform has
// the change, and the Secret gets it on its next sync).
type SecretChange struct {
	App     string `json:"app"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	Synced  bool   `json:"synced"`
	Message string `json:"message,omitempty"`
}

// ListSecrets lists an app's secrets.
func (c *Client) ListSecrets(ctx context.Context, app string) (*SecretsResponse, error) {
	var out SecretsResponse
	return &out, c.get(ctx, app, "secrets", nil, &out)
}

// SetSecret sets one of an app's secrets.
func (c *Client) SetSecret(ctx context.Context, app, name, value string) (*SecretChange, error) {
	var out SecretChange
	_, err := c.do(ctx, http.MethodPut, app, "secrets/"+url.PathEscape(name), nil, map[string]string{"value": value}, &out)
	return &out, err
}

// UnsetSecret removes one of an app's secrets.
func (c *Client) UnsetSecret(ctx context.Context, app, name string) (*SecretChange, error) {
	var out SecretChange
	_, err := c.do(ctx, http.MethodDelete, app, "secrets/"+url.PathEscape(name), nil, nil, &out)
	return &out, err
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

func uobj(apiVersion, kind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range fields {
		o.Object[k] = v
	}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetNamespace(ns)
	o.SetName(name)
	return o
}

// day2 is a fake cluster with app shop (values: its registry entry), a
// Knative service web, a bucket and a domain, and a fake apps API.
type day2 struct {
	t        *testing.T
	kube     *kube.Client
	api      *appapi.Client
	requests []*http.Request
}

func newDay2(t *testing.T, values map[string]any, annotations map[string]string) *day2 {
	t.Helper()
	t.Setenv("MCLOUD_APP", "")
	t.Setenv("MCLOUD_PLATFORM_URL", "off")
	t.Chdir(t.TempDir())
	provider := uobj("fluxcd.controlplane.io/v1", "ResourceSetInputProvider", kube.FluxNamespace, "shop", map[string]any{
		"spec": map[string]any{"defaultValues": values},
	})
	provider.SetLabels(map[string]string{registry.AppLabel: "true"})
	provider.SetAnnotations(annotations)
	cond := func(status, reason, msg string) []any {
		return []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg}}
	}
	ksvc := uobj("serving.knative.dev/v1", "Service", "shop", "web", map[string]any{
		"status": map[string]any{"url": "https://web--shop.makiya.dev", "conditions": cond("True", "", "")},
	})
	bucket := uobj("maxcloud.gronblom.dev/v1alpha1", "Bucket", "shop", "uploads", map[string]any{
		"status": map[string]any{"bucketName": "mc-shop--uploads", "observedGeneration": int64(1), "conditions": cond("True", "Ready", "")},
	})
	domain := uobj("maxcloud.gronblom.dev/v1alpha1", "Domain", "shop", "shop.example.com", map[string]any{
		"spec":   map[string]any{"service": "web", "proxied": true},
		"status": map[string]any{"observedGeneration": int64(1), "conditions": cond("False", "NotAllowed", "shop may not have shop.example.com")},
	})
	gvr := func(g, v, r string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: g, Version: v, Resource: r}
	}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr("fluxcd.controlplane.io", "v1", "resourcesetinputproviders"): "ResourceSetInputProviderList",
		gvr("kustomize.toolkit.fluxcd.io", "v1", "kustomizations"):       "KustomizationList",
		gvr("source.toolkit.fluxcd.io", "v1", "gitrepositories"):         "GitRepositoryList",
		gvr("serving.knative.dev", "v1", "services"):                     "ServiceList",
		gvr("keda.sh", "v1alpha1", "scaledjobs"):                         "ScaledJobList",
		gvr("keda.sh", "v1alpha1", "scaledobjects"):                      "ScaledObjectList",
		gvr("postgresql.cnpg.io", "v1", "clusters"):                      "ClusterList",
		gvr("maxcloud.gronblom.dev", "v1alpha1", "buckets"):              "BucketList",
		gvr("maxcloud.gronblom.dev", "v1alpha1", "domains"):              "DomainList",
		gvr("maxcloud.gronblom.dev", "v1alpha1", "previews"):             "PreviewList",
		gvr("maxcloud.gronblom.dev", "v1alpha1", "databases"):            "DatabaseList",
		gvr("maxcloud.gronblom.dev", "v1alpha1", "databasebranches"):     "DatabaseBranchList",
	}, provider, ksvc, bucket, domain)
	core := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	d := &day2{t: t, kube: &kube.Client{Core: core, Dyn: dyn}}

	api := http.NewServeMux()
	api.HandleFunc("GET /apps/shop/logs", func(w http.ResponseWriter, r *http.Request) {
		d.requests = append(d.requests, r)
		json.NewEncoder(w).Encode(appapi.LogsResponse{App: "shop", Entries: []appapi.LogEntry{
			{Time: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), Pod: "web-00001-deployment-a-b", Container: "user-container", Line: "started"},
			{Time: time.Date(2026, 11, 17, 10, 0, 1, 0, time.UTC), Pod: "web-00001-deployment-a-b", Container: "user-container", Line: "GET / 500"},
		}})
	})
	api.HandleFunc("GET /apps/shop/metrics", func(w http.ResponseWriter, r *http.Request) {
		d.requests = append(d.requests, r)
		ratio, p95, pods := 0.1, 0.42, 1.0
		json.NewEncoder(w).Encode(appapi.MetricsResponse{App: "shop", Window: r.URL.Query().Get("window"),
			Services: []appapi.ServiceMetrics{{Name: "web", Requests: 50, RequestsPerSecond: 50.0 / 3600, Errors: 5, ErrorRatio: &ratio, P95Seconds: &p95, Pods: &pods}},
			Queues:   []appapi.QueueMetrics{{Name: "mail", Kind: "scaledjob", Backlog: 3}},
			Jobs:     appapi.JobMetrics{Running: 1}})
	})
	api.HandleFunc("GET /apps/shop/alerts", func(w http.ResponseWriter, r *http.Request) {
		d.requests = append(d.requests, r)
		json.NewEncoder(w).Encode(appapi.AlertsResponse{App: "shop", Alerts: []appapi.Alert{
			{Name: "KnativeServiceErrors", State: "firing", Summary: "shop/web: 10% of requests fail with 5xx", Labels: map[string]string{"kn_service_name": "web"}},
		}})
	})
	api.HandleFunc("GET /apps/other/alerts", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"authelia:ann may not list pods in namespace other"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	d.api = &appapi.Client{Base: srv.URL + "/apps/", HTTP: srv.Client()}
	return d
}

func (d *day2) run(withAPI bool, args ...string) (string, error) {
	d.t.Helper()
	var out bytes.Buffer
	g := &globals{in: strings.NewReader(""), out: &out, errOut: io.Discard, plain: true, testKube: d.kube}
	if withAPI {
		g.testAppAPI = d.api
	}
	cmd := newRoot(g)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return out.String(), err
}

func TestStatusShowsSyncErrorBucketsDomainsAlerts(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "repo": "o/shop", "owners": "ann"}, map[string]string{
		kube.SyncErrorAnnotation: "quota.cpu: 4 is more than a developer may ask for (1)", kube.SyncedRevisionAnnotation: "main@sha1:0123456789abcdef",
	})
	out, err := d.run(true, "status", "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"not applied: quota.cpu: 4 is more than a developer may ask for (1)", "0123456",
		"mc-shop--uploads", "https://shop.example.com/", "NotAllowed: shop may not have shop.example.com",
		"ALERTS", "shop/web: 10% of requests fail with 5xx",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	out, err = d.run(true, "status", "shop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Name    string   `json:"name"`
		Mode    string   `json:"mode"`
		Owners  []string `json:"owners"`
		Sync    syncJSON `json:"sync"`
		Buckets []struct {
			Name  string   `json:"name"`
			Ready condJSON `json:"ready"`
		} `json:"buckets"`
		Domains []struct {
			Host  string   `json:"host"`
			Ready condJSON `json:"ready"`
		} `json:"domains"`
		Services []serviceJSON  `json:"services"`
		Alerts   []appapi.Alert `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if st.Name != "shop" || st.Mode != "gitops" || st.Owners[0] != "ann" || !strings.Contains(st.Sync.Error, "quota.cpu") ||
		st.Buckets[0].Name != "uploads" || st.Domains[0].Ready.Reason != "NotAllowed" || st.Services[0].Name != "web" || st.Alerts[0].Name != "KnativeServiceErrors" {
		t.Errorf("status --json:\n%s", out)
	}

	// Without the API (an older platform): no alerts, nothing else lost.
	out, err = d.run(false, "status", "shop", "--json")
	if err != nil || !strings.Contains(out, `"alerts": null`) || !strings.Contains(out, `"uploads"`) {
		t.Errorf("without the API: %v\n%s", err, out)
	}
}

func TestAppsListJSON(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "deploy": "direct", "owners": "ann,bob"}, nil)
	out, err := d.run(false, "apps", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Apps []appJSON `json:"apps"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Apps) != 1 {
		t.Fatalf("%v:\n%s", err, out)
	}
	a := got.Apps[0]
	if a.Name != "shop" || a.Mode != "direct" || strings.Join(a.Owners, ",") != "ann,bob" || a.RegisteredIn != "cluster" || a.Deploy != nil {
		t.Errorf("%+v", a)
	}
}

func TestLogsFromLoki(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "repo": "o/shop"}, nil)
	out, err := d.run(true, "logs", "shop", "web", "--since", "2h", "--timestamps", "--tail", "50")
	if err != nil {
		t.Fatal(err)
	}
	want := "web-00001-deployment-a-b 2026-11-17T10:00:00Z started\nweb-00001-deployment-a-b 2026-11-17T10:00:01Z GET / 500\n"
	if out != want {
		t.Errorf("got:\n%s", out)
	}
	q := d.requests[0].URL.Query()
	if q.Get("pod") != "web-[0-9]+-deployment-.+" || q.Get("limit") != "50" || q.Get("since") == "" || q.Get("until") != "" {
		t.Errorf("query %v", q)
	}
	// --until reads Loki even without --since; --selector can't.
	if _, err := d.run(true, "logs", "shop", "--until", "30m"); err != nil {
		t.Error(err)
	}
	if _, err := d.run(true, "logs", "shop", "--source", "loki", "-l", "a=b"); err == nil {
		t.Error("--selector with Loki should fail")
	}
	// Without the API, --source loki fails and auto falls back to the
	// pods (none here, so that fails too, saying so).
	if _, err := d.run(false, "logs", "shop", "--source", "loki"); err == nil || !strings.Contains(err.Error(), "doesn't offer") {
		t.Errorf("no API, loki: %v", err)
	}
	if _, err := d.run(false, "logs", "shop", "--since", "2h"); err == nil || !strings.Contains(err.Error(), "no pods") {
		t.Errorf("no API, auto: %v", err)
	}
}

func TestMetricsAndAlerts(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "repo": "o/shop"}, nil)
	out, err := d.run(true, "metrics", "shop", "--window", "24h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"web", "50", "5 (10.0%)", "420ms", "mail", "scaledjob", "1 running, 0 failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lacks %q:\n%s", want, out)
		}
	}
	if w := d.requests[len(d.requests)-1].URL.Query().Get("window"); w != "24h" {
		t.Errorf("window %q", w)
	}
	out, err = d.run(true, "metrics", "shop", "--json")
	var m appapi.MetricsResponse
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m.Services[0].Name != "web" {
		t.Errorf("metrics --json: %v\n%s", err, out)
	}

	out, err = d.run(true, "alerts", "shop")
	if err != nil || !strings.Contains(out, "shop/web: 10% of requests fail with 5xx") || !strings.Contains(out, "kn_service_name=web") {
		t.Errorf("alerts: %v\n%s", err, out)
	}
	if _, err := d.run(true, "alerts", "other"); err == nil || !strings.Contains(err.Error(), "may not") {
		t.Errorf("someone else's app: %v", err)
	}
	if _, err := d.run(false, "metrics", "shop"); err == nil || !strings.Contains(err.Error(), "Grafana") {
		t.Errorf("no API: %v", err)
	}
}

func TestDirectHistoryAndRollbackDryRun(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "deploy": "direct"}, nil)
	ctx := context.Background()
	for i, img := range []string{"r/shop@sha256:aaaaaaaaaaaaaaaa", "r/shop@sha256:bbbbbbbbbbbbbbbb"} {
		_, err := d.kube.RecordDeploy(ctx, "shop", kube.DirectDeploy{Time: time.Now().Add(time.Duration(i-2) * time.Hour), Image: img, Functions: []string{"http/web"}},
			[]map[string]any{{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": "web"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := d.run(false, "history", "shop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var h historyJSON
	if err := json.Unmarshal([]byte(out), &h); err != nil || h.Mode != "direct" || len(h.Deploys) != 2 || h.Deploys[0].ID != "2" || !h.Deploys[0].Current || h.Deploys[1].Current {
		t.Fatalf("%v:\n%s", err, out)
	}
	out, err = d.run(false, "rollback", "shop", "--dry-run")
	if err != nil || !strings.Contains(out, "Back to") || !strings.Contains(out, "aaaaaaaaaaaa") {
		t.Errorf("rollback --dry-run: %v\n%s", err, out)
	}
	if _, err := d.run(false, "rollback", "shop", "--to", "2", "--dry-run"); err == nil || !strings.Contains(err.Error(), "deployed now") {
		t.Errorf("rollback to the current one: %v", err)
	}
	if _, err := d.run(false, "rollback", "shop", "--to", "9", "--dry-run"); err == nil {
		t.Error("rollback to a deploy that isn't there should fail")
	}
}

func TestRollbackTargetGitOps(t *testing.T) {
	h := &historyJSON{App: "shop", Mode: "gitops", Deploys: []historyEntry{
		{ID: "cccc111"}, {ID: "bbbb222", Current: true}, {ID: "aaaa333"}, {ID: "aaaa444"},
	}}
	// Flux applied bbbb (cccc isn't applied yet): back from bbbb is aaaa333.
	if cur, tg, err := rollbackTarget(h, ""); err != nil || cur.ID != "bbbb222" || tg.ID != "aaaa333" {
		t.Errorf("default: %v %v %v", cur, tg, err)
	}
	if _, tg, err := rollbackTarget(h, "aaaa4"); err != nil || tg.ID != "aaaa444" {
		t.Errorf("prefix: %v %v", tg, err)
	}
	if _, _, err := rollbackTarget(h, "aaaa"); err == nil {
		t.Error("an ambiguous prefix should fail")
	}
	if _, _, err := rollbackTarget(h, "bbbb222"); err == nil {
		t.Error("the current one should fail")
	}
}

func TestImageOfAndAppliedSHA(t *testing.T) {
	if got := imageOf("deploy: abc1234\n\nImage registry.example/shop/shop@sha256:0123"); got != "registry.example/shop/shop@sha256:0123" {
		t.Errorf("imageOf = %q", got)
	}
	if got := appliedSHA("main@sha1:0123456789"); got != "0123456789" {
		t.Errorf("appliedSHA = %q", got)
	}
}

func TestAPIBase(t *testing.T) {
	const doc, gw = "https://maxcloud.example/.well-known/maxcloud.json", "https://api.example"
	for _, c := range []struct{ doc, gateway, apps, want string }{
		{doc, gw, "https://api.example/api/v1/apps/", "https://api.example/api/v1/apps/"}, // on the gateway
		{doc, "", "/api/v1/apps/", "https://maxcloud.example/api/v1/apps/"},               // a path on the document's host
		{"http://127.0.0.1:8082/.well-known/maxcloud.json", "", "/api/v1/apps/", "http://127.0.0.1:8082/api/v1/apps/"},
		{"http://maxcloud.example/.well-known/maxcloud.json", "", "/api/v1/apps/", ""}, // the token only over HTTPS
		{doc, gw, "https://evil.example/x", ""},                                        // nowhere but the gateway or the document's host
		{doc, gw, "http://api.example/api/v1/apps/", ""},                               // the gateway, but not over HTTPS
		{doc, "", "https://api.example/api/v1/apps/", ""},                              // a gateway the document doesn't name
	} {
		got, err := apiBase(c.doc, c.gateway, c.apps)
		if c.want == "" && err == nil || c.want != "" && got != c.want {
			t.Errorf("%s (gateway %q) + %s = %q, %v", c.doc, c.gateway, c.apps, got, err)
		}
	}
}

func TestQueueOf(t *testing.T) {
	q := queueOf("shop", "failed", "mail\t1\t0\t2\t40\n", "41\tmail\tfailed\t3\t2026-11-17 10:00\tboom\t2026-11-17T10:00:00Z\n")
	if len(q.Queues) != 1 || q.Queues[0].Failed != 2 || q.Queues[0].Done != 40 || len(q.Jobs) != 1 || q.Jobs[0].ID != 41 || q.Jobs[0].Created != "2026-11-17T10:00:00Z" || q.Jobs[0].LastError != "boom" {
		t.Errorf("%+v", q)
	}
}

func TestEnvAndSecretsListJSON(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: info\n")
	if _, err := e.run("", "env", "set", "API_BASE=https://api.example.com", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	out, err := e.run("", "env", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "app": "shop",
  "variables": [
    {
      "name": "LOG_LEVEL",
      "value": "info",
      "from": "maxcloud.yaml"
    },
    {
      "name": "API_BASE",
      "value": "https://api.example.com",
      "from": "mcloud"
    }
  ]
}
`
	if out != want {
		t.Errorf("env list --json:\n%s", out)
	}
	if _, err := e.run("TOKEN-VALUE\n", "secrets", "set", "TOKEN", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	out, err = e.run("", "secrets", "list", "--json")
	if err != nil || out != "{\n  \"app\": \"shop\",\n  \"secrets\": [\n    {\n      \"name\": \"TOKEN\"\n    }\n  ]\n}\n" {
		t.Errorf("secrets list --json: %v\n%s", err, out)
	}
}

func TestBranchListJSON(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop"}, nil)
	if err := d.kube.CreateBranch(context.Background(), "shop", "try", nil); err != nil {
		t.Fatal(err)
	}
	out, err := d.run(false, "db", "branch", "list", "shop", "--json")
	if err != nil || !strings.Contains(out, `"name": "try"`) || !strings.Contains(out, `"branches": [`) {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestGitOpsHistory(t *testing.T) {
	d := newDay2(t, map[string]any{"name": "shop", "repo": "o/shop"}, nil)
	k := uobj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", kube.FluxNamespace, "shop", map[string]any{
		"status": map[string]any{"lastAppliedRevision": "main@sha1:bbbb000000000000000000000000000000000000"},
	})
	if _, err := d.kube.Dyn.Resource(kube.GVRKustomization).Namespace(kube.FluxNamespace).Create(context.Background(), k, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	commit := func(sha, msg string) map[string]any {
		return map[string]any{"sha": sha, "commit": map[string]any{"message": msg, "author": map[string]string{"name": "bot"}, "committer": map[string]string{"date": "2026-11-17T10:00:00Z"}}}
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/shop/commits" || r.URL.Query().Get("path") != "deploy/k8s" {
			http.Error(w, "unexpected "+r.URL.String(), 500)
			return
		}
		all := []any{
			commit("cccc000000000000000000000000000000000000", "deploy: 3333333\n\nImage r/shop/shop@sha256:cccccccccccccccc"),
			commit("bbbb000000000000000000000000000000000000", "deploy: 2222222\n\nImage r/shop/shop@sha256:bbbbbbbbbbbbbbbb"),
			commit("aaaa000000000000000000000000000000000000", "deploy: 1111111\n\nImage r/shop/shop@sha256:aaaaaaaaaaaaaaaa"),
		}
		if r.URL.Query().Get("sha") == "bbbb000000000000000000000000000000000000" {
			all = all[1:]
		}
		json.NewEncoder(w).Encode(all)
	}))
	t.Cleanup(gh.Close)
	c := github.New("token")
	c.BaseURL = gh.URL
	var out bytes.Buffer
	g := &globals{in: strings.NewReader(""), out: &out, errOut: io.Discard, plain: true, testKube: d.kube, testGitHub: c}
	cmd := newRoot(g)
	cmd.SetArgs([]string{"rollback", "shop", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	// Flux applied bbbb, so the one before it is aaaa, not bbbb.
	if !strings.Contains(out.String(), "Now      bbbb000") || !strings.Contains(out.String(), "Back to  aaaa000") || !strings.Contains(out.String(), "[skip ci]") {
		t.Errorf("rollback --dry-run:\n%s", out.String())
	}
}

// Package live checks generated manifests against a real cluster with
// server-side dry runs. Skipped unless MCLOUD_LIVE_KUBECONFIG is set; it
// persists nothing.
package live

import (
	"context"
	"os"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

const desc = `{"sdk":"test","version":"0","functions":[
 {"name":"live-api","export":"api","kind":"http","memory":"128Mi","cpu":null,"public":false,"minInstances":0,"maxInstances":2,"concurrency":0,"timeoutSeconds":300},
 {"name":"live-cron","export":"cron","kind":"schedule","memory":null,"cpu":null,"schedule":"0 3 * * *","timeZone":"UTC","retries":0,"timeoutSeconds":600},
 {"name":"live-worker","export":"w","kind":"queue","memory":null,"cpu":null,"queue":"live","concurrency":1,"maxInstances":2,"maxAttempts":5,"backoffSeconds":10,"timeoutSeconds":300,"backlogQuery":"SELECT 1"}]}`

func TestDirectApplyDryRun(t *testing.T) {
	cfg := os.Getenv("MCLOUD_LIVE_KUBECONFIG")
	ns := os.Getenv("MCLOUD_LIVE_NAMESPACE")
	if cfg == "" || ns == "" {
		t.Skip("set MCLOUD_LIVE_KUBECONFIG and MCLOUD_LIVE_NAMESPACE (an app namespace)")
	}
	k, err := kube.New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	k.DryRun = true
	d, err := functions.Parse([]byte(desc))
	if err != nil {
		t.Fatal(err)
	}
	db, err := functions.DatabaseObjects()
	if err != nil {
		t.Fatal(err)
	}
	objs := functions.Objects(d, functions.Options{
		Image:    "ghcr.io/maxemiliang/fn-demo@sha256:2dd7d61dd23dd5e87917a2d65775afd360c43f16ebb2882c9934ed78aa233828",
		Database: true,
		Labels:   map[string]string{kube.DirectLabel: kube.FieldManager},
	})
	for _, o := range append(db, objs...) {
		if err := k.Apply(context.Background(), ns, []map[string]any{o}); err != nil {
			t.Error(err)
		}
	}
}

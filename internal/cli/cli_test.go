package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

func TestAppNameFromProjectFile(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte("app: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	t.Setenv("MCLOUD_APP", "")
	if got, err := appName(nil); err != nil || got != "shop" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, _ := appName([]string{"other"}); got != "other" {
		t.Errorf("argument should win, got %q", got)
	}
	t.Chdir(t.TempDir())
	if _, err := appName(nil); err == nil {
		t.Error("want an error without a project file")
	}
}

func TestDiffEntries(t *testing.T) {
	a := &registry.Entry{Name: "x", Repo: "o/x", CPU: "500m"}
	b := &registry.Entry{Name: "x", Repo: "o/x", Memory: "2Gi"}
	got := strings.Join(diffEntries(a, b), "; ")
	want := "cpu 500m -> 200m (default); memory 1Gi (default) -> 2Gi"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestShortRev(t *testing.T) {
	for in, want := range map[string]string{
		"refs/heads/main@sha1:2495009b807d32e27fa7768b62490bb44ddcd991": "main@2495009",
		"v1.6.3@sha1:c0836133e24073fcdcdbb1713aba3cc73a89d0b5":          "v1.6.3@c083613",
		"": "-",
	} {
		if got := shortRev(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestSource(t *testing.T) {
	if got := source(&registry.Entry{Name: "d", Path: "apps/d"}, "o/homelab"); got != "o/homelab:apps/d" {
		t.Error(got)
	}
	if got := source(&registry.Entry{Name: "s", Repo: "o/s", Branch: "prod"}, "o/homelab"); got != "o/s@prod:deploy/k8s" {
		t.Error(got)
	}
}

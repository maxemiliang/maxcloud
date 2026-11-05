package cli

import (
	"encoding/json"
	"github.com/maxemiliang/maxcloud/internal/auth"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxemiliang/maxcloud/internal/registry"
	"sigs.k8s.io/yaml"
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

func TestInitWritesStarters(t *testing.T) {
	for _, lang := range []string{"node", "go"} {
		dir := filepath.Join(t.TempDir(), "my-app")
		os.MkdirAll(dir, 0o755)
		t.Chdir(dir)
		g := &globals{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard}
		cmd := initCmd(g)
		cmd.SetArgs([]string{"--lang", lang})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		for _, p := range []string{"maxcloud.yaml", ".github/workflows/deploy.yaml", "deploy/k8s/kustomization.yaml"} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: no %s", lang, p)
			}
		}
		y, _ := os.ReadFile("maxcloud.yaml")
		if !strings.Contains(string(y), "app: my-app\n") {
			t.Errorf("%s: maxcloud.yaml:\n%s", lang, y)
		}
		if lang == "go" {
			m, _ := os.ReadFile("main.go")
			if !strings.Contains(string(m), `"Hello from %s\n"`) {
				t.Errorf("main.go:\n%s", m)
			}
			gm, _ := os.ReadFile("go.mod")
			if !strings.HasPrefix(string(gm), "module my-app\n") {
				t.Errorf("go.mod:\n%s", gm)
			}
		} else {
			pj, _ := os.ReadFile("package.json")
			if !strings.Contains(string(pj), `"name": "my-app"`) {
				t.Errorf("package.json:\n%s", pj)
			}
		}
	}
}

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"15m":                       now.Add(-15 * time.Minute),
		"2h30m":                     now.Add(-150 * time.Minute),
		"2026-11-05T10:00:00Z":      time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC),
		"2026-11-05T11:00:00+02:00": time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
	} {
		got, err := parseAt(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseAt(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	local, err := parseAt("2026-11-05 10:00", now.Add(48*time.Hour))
	if err != nil || local.Hour() != 10 || local.Location() != time.Local {
		t.Errorf("local time: %v, %v", local, err)
	}
	for _, bad := range []string{"", "-5m", "0s", "tomorrow", "2026-11-06T00:00:00Z"} {
		if _, err := parseAt(bad, now); err == nil {
			t.Errorf("parseAt(%q): want an error", bad)
		}
	}
}

func TestProjectEntry(t *testing.T) {
	read := func(y string) *project {
		t.Helper()
		var p project
		if err := yaml.Unmarshal([]byte(y), &p); err != nil {
			t.Fatal(err)
		}
		return &p
	}
	// A new app: everything from maxcloud.yaml.
	p := read("app: shop\nrepo: o/shop\nquota: {cpu: 500m}\nbuckets: [uploads, assets:public]\ndb: {size: 2Gi, instances: 2}\n")
	e := p.entry(nil)
	if e.Name != "shop" || e.Repo != "o/shop" || e.CPU != "500m" || e.Buckets != "uploads,assets:public" || e.DB != "true" || e.DBSize != "2Gi" || e.DBInstances != "2" {
		t.Errorf("new entry = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Error(err)
	}

	// An existing app: what maxcloud.yaml leaves out stays.
	base := &registry.Entry{Name: "shop", Repo: "o/shop", CPU: "1", Memory: "2Gi", Buckets: "old", DB: "true", DBSize: "5Gi"}
	e = read("app: shop\nbuckets: [new]\n").entry(base)
	if e.CPU != "1" || e.Memory != "2Gi" || e.Buckets != "new" || e.DB != "true" || e.DBSize != "5Gi" {
		t.Errorf("merged = %+v", e)
	}
	// db: true keeps the size; db: false removes the database.
	if e := read("app: shop\ndb: true\n").entry(base); e.DB != "true" || e.DBSize != "5Gi" {
		t.Errorf("db: true = %+v", e)
	}
	if e := read("app: shop\ndb: false\n").entry(base); e.DB != "" || e.DBSize != "" {
		t.Errorf("db: false = %+v", e)
	}
	// A direct app has no git source.
	if e := read("app: quick\ndeploy: direct\n").entry(&registry.Entry{Name: "quick", Repo: "o/q"}); e.Repo != "" || !e.Direct() {
		t.Errorf("direct = %+v", e)
	}
	// Bad db value.
	var bad project
	if err := yaml.Unmarshal([]byte("app: x\ndb: maybe\n"), &bad); err == nil {
		t.Error("db: maybe accepted")
	}
}

func TestCredentialPrintsTheCachedToken(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := auth.Config{Issuer: auth.DefaultIssuer, ClientID: auth.DefaultClientID}
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := auth.Save(cfg, &auth.Tokens{IDToken: "the-id-token", Expiry: exp}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	g := &globals{in: strings.NewReader(""), out: &out, errOut: io.Discard}
	c := credentialCmd(g)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var cred struct {
		APIVersion, Kind string
		Status           struct {
			Token               string
			ExpirationTimestamp time.Time
		}
	}
	if err := json.Unmarshal([]byte(out.String()), &cred); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if cred.Kind != "ExecCredential" || cred.APIVersion != "client.authentication.k8s.io/v1" || cred.Status.Token != "the-id-token" {
		t.Errorf("credential = %+v", cred)
	}
	if !cred.Status.ExpirationTimestamp.Before(exp) {
		t.Errorf("expires %v, want before the token's %v", cred.Status.ExpirationTimestamp, exp)
	}
}

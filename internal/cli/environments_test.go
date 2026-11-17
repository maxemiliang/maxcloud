package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

const envProject = `app: shop
buckets: [uploads]
env:
  LOG_LEVEL: info
  RATIO: 0.10
environments:
  staging:
    quota: {cpu: 100m}
    db: {size: 1Gi}
    env: {LOG_LEVEL: debug}
  qa:
    branch: release/qa
    buckets: []
    db: false
    domains: [{host: qa.example.com, service: web}]
previews:
  ttl: 48h
  db: true
  env: {PREVIEW: "yes"}
`

func TestProjectEnvironments(t *testing.T) {
	p, err := parseProject([]byte(envProject))
	if err != nil {
		t.Fatal(err)
	}
	p.App = "shop"
	if err := p.checkEnvironments(); err != nil {
		t.Fatal(err)
	}
	// The same JSON the controller's app sync writes.
	want := `{"qa":{"branch":"release/qa","buckets":"","db":"false","domains":"qa.example.com:web"},"staging":{"cpu":"100m","db":"true","dbSize":"1Gi"}}`
	if got := p.environmentsValue(); got != want {
		t.Errorf("environments =\n%s\nwant\n%s", got, want)
	}
	e := p.entry(&registry.Entry{Name: "shop", Repo: "ann/shop", Owners: "ann"})
	if e.Environments != want || e.Previews != "true" || e.PreviewTTL != "48h" || e.PreviewDB != "true" || e.PreviewMax != "" || e.Owners != "ann" {
		t.Errorf("entry = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Error(err)
	}
	if got := strings.Join(p.syncedFields(), ","); got != "buckets,environments,previewDB,previewMax,previewTTL,previews" {
		t.Errorf("synced fields %s", got)
	}

	// Variables: the environment's over the app's, as written.
	if got := p.envFor("staging", false); got["LOG_LEVEL"] != "debug" || got["RATIO"] != "0.10" {
		t.Errorf("staging's env %v", got)
	}
	if got := p.envFor("qa", false); got["LOG_LEVEL"] != "info" {
		t.Errorf("qa's env %v", got)
	}
	if got := p.envFor("", true); got["PREVIEW"] != "yes" || got["LOG_LEVEL"] != "info" {
		t.Errorf("previews' env %v", got)
	}

	if b := p.environmentBranch("staging"); b != "staging" {
		t.Errorf("staging's branch %s", b)
	}
	if env, ok := p.environmentForBranch("release/qa"); !ok || env != "qa" {
		t.Errorf("release/qa is %q, %v", env, ok)
	}
	if _, ok := p.environmentForBranch("main"); ok {
		t.Error("main is an environment's branch")
	}

	// No environments: "{}" (an absent key leaves them as they are).
	q, _ := parseProject([]byte("app: x\nenvironments: {}\npreviews: false\n"))
	e = q.entry(&registry.Entry{Name: "x", Environments: want, Previews: "true", PreviewTTL: "1h"})
	if e.Environments != "{}" || e.Previews != "" || e.PreviewTTL != "" {
		t.Errorf("cleared: %+v", e)
	}
	q, _ = parseProject([]byte("app: x\n"))
	if e = q.entry(&registry.Entry{Name: "x", Environments: want}); e.Environments != want {
		t.Errorf("an absent environments: changed them: %+v", e)
	}
}

func TestProjectEnvironmentsRefused(t *testing.T) {
	for _, file := range []string{
		"app: shop\nenvironments: {pr-1: {}}\n",
		"app: shop\nenvironments: {Staging: {}}\n",
		"app: shop\nenvironments: {waytoolongname: {}}\n",
		"app: shop\nenvironments: {staging: {branch: main}}\n",
		"app: shop\nenvironments: {main: {}}\n",
		"app: shop\nenvironments: {staging: {branch: 'a..b'}}\n",
	} {
		p, err := parseProject([]byte(file))
		if err != nil {
			continue
		}
		if err := p.checkEnvironments(); err == nil {
			t.Errorf("%q: accepted", file)
		}
	}
	if _, err := parseProject([]byte("app: shop\nenvironments: {staging: {env: [1]}}\n")); err == nil {
		t.Error("an env list accepted")
	}
}

func TestWithEnvironment(t *testing.T) {
	t.Setenv("MCLOUD_ENV", "")
	envFlag = ""
	if got, _ := withEnvironment("shop"); got != "shop" {
		t.Errorf("no env: %s", got)
	}
	envFlag = "staging"
	t.Cleanup(func() { envFlag = "" })
	if got, _ := withEnvironment("shop"); got != "shop--staging" {
		t.Errorf("--env staging: %s", got)
	}
	if got, err := withEnvironment("shop--staging"); err != nil || got != "shop--staging" {
		t.Errorf("shop--staging with --env staging: %s, %v", got, err)
	}
	if _, err := withEnvironment("shop--qa"); err == nil {
		t.Error("shop--qa with --env staging accepted")
	}
	envFlag = "Bad_Name"
	if _, err := withEnvironment("shop"); err == nil {
		t.Error("a bad environment name accepted")
	}
	envFlag = ""
	t.Setenv("MCLOUD_ENV", "pr-42")
	if got, _ := appName([]string{"shop"}); got != "shop--pr-42" {
		t.Errorf("$MCLOUD_ENV: %s", got)
	}
}

func prEvent(t *testing.T, mod func(map[string]any)) *pullRequestEvent {
	t.Helper()
	ev := map[string]any{
		"action": "synchronize", "number": 42,
		"pull_request": map[string]any{
			"head":   map[string]any{"sha": "abcdef0123", "repo": map[string]any{"full_name": "ann/shop"}},
			"base":   map[string]any{"repo": map[string]any{"full_name": "ann/shop"}},
			"labels": []any{map[string]any{"name": "preview"}},
		},
	}
	if mod != nil {
		mod(ev)
	}
	data, _ := json.Marshal(ev)
	var out pullRequestEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestDecidePreview(t *testing.T) {
	on, _ := parseProject([]byte("app: shop\npreviews: true\n"))
	off, _ := parseProject([]byte("app: shop\n"))
	pr := func(m map[string]any) map[string]any { return m["pull_request"].(map[string]any) }
	for _, tc := range []struct {
		name  string
		event string
		mod   func(map[string]any)
		p     *project
		want  previewDecision
		err   bool
	}{
		{"labelled, same repo", "pull_request", nil, on, previewBuild, false},
		{"pull_request_target", "pull_request_target", nil, on, previewSkip, true},
		{"closed", "pull_request", func(m map[string]any) { m["action"] = "closed" }, off, previewRemove, false},
		{"fork", "pull_request", func(m map[string]any) {
			pr(m)["head"].(map[string]any)["repo"] = map[string]any{"full_name": "eve/shop"}
		}, on, previewSkip, false},
		{"deleted fork", "pull_request", func(m map[string]any) { pr(m)["head"].(map[string]any)["repo"] = nil }, on, previewSkip, false},
		{"no label", "pull_request", func(m map[string]any) { pr(m)["labels"] = []any{map[string]any{"name": "bug"}} }, on, previewSkip, false},
		{"previews off", "pull_request", nil, off, previewSkip, false},
		{"no project", "pull_request", nil, nil, previewSkip, false},
	} {
		got, why, err := decidePreview(tc.event, prEvent(t, tc.mod), tc.p)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("%s: %v, %q, %v; want %v (error %v)", tc.name, got, why, err, tc.want, tc.err)
		}
		if got == previewSkip && err == nil && why == "" {
			t.Errorf("%s: skipped without saying why", tc.name)
		}
	}
}

// The preview's commit holds only deploy/, has no parent, and leaves the
// checkout and its index alone.
func TestCommitTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "no-config"))
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.name", "t"}, {"config", "user.email", "t@example.com"},
	} {
		if _, err := git(args...); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile("app.go", []byte("package main\n"), 0o644)
	git("add", "app.go")
	git("commit", "-q", "-m", "app")
	os.WriteFile("staged.txt", []byte("x"), 0o644)
	git("add", "staged.txt")

	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, "deploy", "k8s"), 0o755)
	os.WriteFile(filepath.Join(work, "deploy", "k8s", "kustomization.yaml"), []byte("resources: []\n"), 0o644)
	sha, err := commitTree(work, "deploy", "preview: #42")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := git("ls-tree", "-r", "--name-only", sha)
	if files != "deploy/k8s/kustomization.yaml" {
		t.Errorf("the commit has %q", files)
	}
	if parents, _ := git("rev-list", "--parents", "-n1", sha); strings.Contains(parents, " ") {
		t.Errorf("the commit has a parent: %s", parents)
	}
	if staged, _ := git("diff", "--cached", "--name-only"); staged != "staged.txt" {
		t.Errorf("the checkout's index changed: %q", staged)
	}
	if head, _ := git("log", "-1", "--format=%s"); head != "app" {
		t.Errorf("HEAD moved: %s", head)
	}
}

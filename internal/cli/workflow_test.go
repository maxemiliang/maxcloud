package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/github"
)

func writeWorkflows(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// An app deploying from its CI workflow (lankkupaja's ci.yml): `on: push`,
// a test job, and a job that calls run-deploy.
const ciWorkflow = `name: ci
on: push
jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - run: pnpm test
  maxcloud:
    needs: ci
    uses: maxemiliang/maxcloud/.github/workflows/run-deploy.yaml@main
    with:
      manifests: deploy/maxcloud
`

const lintWorkflow = `name: lint
on: [push, workflow_dispatch]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: make lint
`

func TestFindDeployWorkflow(t *testing.T) {
	for _, c := range []struct {
		name     string
		files    map[string]string
		path     string
		dispatch bool
		err      string
	}{
		{"mcloud init's", map[string]string{"deploy.yaml": appFiles[".github/workflows/deploy.yaml"]}, ".github/workflows/deploy.yaml", true, ""},
		{"CI workflow, on: push", map[string]string{"ci.yml": ciWorkflow, "lint.yaml": lintWorkflow}, ".github/workflows/ci.yml", false, ""},
		{"on: list", map[string]string{"ci.yml": strings.Replace(ciWorkflow, "on: push", "on: [push, workflow_dispatch]", 1)}, ".github/workflows/ci.yml", true, ""},
		{"on: string", map[string]string{"ci.yml": strings.Replace(ciWorkflow, "on: push", "on: workflow_dispatch", 1)}, ".github/workflows/ci.yml", true, ""},
		{"on: map", map[string]string{"ci.yml": strings.Replace(ciWorkflow, "on: push", "on:\n  push:\n  workflow_dispatch: {}", 1)}, ".github/workflows/ci.yml", true, ""},
		{"other ref", map[string]string{"ci.yml": strings.Replace(ciWorkflow, "@main", "@v0.14.1", 1)}, ".github/workflows/ci.yml", false, ""},
		{"none", map[string]string{"lint.yaml": lintWorkflow}, "", false, "no workflow"},
		{"no workflows", nil, "", false, "no workflow"},
		{"several", map[string]string{"ci.yml": ciWorkflow, "deploy.yaml": appFiles[".github/workflows/deploy.yaml"]}, "", false, ".github/workflows/ci.yml, .github/workflows/deploy.yaml"},
		{"not a workflow", map[string]string{"ci.yml": ciWorkflow, "notes.txt": "uses: " + runDeployRef + "main"}, ".github/workflows/ci.yml", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			wf, err := findDeployWorkflow(writeWorkflows(t, c.files))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one with %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if wf.Path != c.path || wf.Dispatch != c.dispatch {
				t.Errorf("= %+v, want %s dispatch=%v", wf, c.path, c.dispatch)
			}
			if wf.File() != filepath.Base(c.path) {
				t.Errorf("File() = %s", wf.File())
			}
		})
	}
	// No .github at all.
	if _, err := findDeployWorkflow(t.TempDir()); err == nil {
		t.Error("no error without .github/workflows")
	}
}

func TestDeployRun(t *testing.T) {
	wf := &deployWorkflow{Path: ".github/workflows/ci.yml"}
	runs := []github.Run{
		// Another workflow called deploy: the name doesn't count, the file does.
		{ID: 1, Name: "deploy", Path: ".github/workflows/deploy.yaml", Event: "push"},
		{ID: 2, Name: "ci", Path: ".github/workflows/ci.yml@refs/heads/main", Event: "workflow_dispatch"},
		{ID: 3, Name: "ci", Path: ".github/workflows/ci.yml", Event: "push"},
	}
	if r := deployRun(runs, wf, false); r == nil || r.ID != 2 {
		t.Errorf("push: %+v, want the newest ci.yml run (2)", r)
	}
	if r := deployRun(runs[2:], wf, true); r != nil {
		t.Errorf("dispatched: %+v, want none (only a push ran)", r)
	}
	if r := deployRun(runs, wf, true); r == nil || r.ID != 2 {
		t.Errorf("dispatched: %+v, want 2", r)
	}
	if r := deployRun(runs[:1], wf, false); r != nil {
		t.Errorf("other workflow: %+v, want none", r)
	}
}

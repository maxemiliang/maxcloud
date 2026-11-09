package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/maxemiliang/maxcloud/internal/github"
)

// runDeployRef is how an app's workflow calls maxcloud's reusable deploy
// workflow, at any ref: the one thing every deploy workflow has, whatever
// its file or name (mcloud init writes deploy.yaml; an app may deploy from
// its CI workflow instead).
const runDeployRef = "maxemiliang/maxcloud/.github/workflows/run-deploy.yaml@"

// deployWorkflow is the app repo's workflow that deploys it.
type deployWorkflow struct {
	// Path from the repo root, as GitHub reports a run's workflow:
	// .github/workflows/deploy.yaml.
	Path string
	// Dispatch: its on: has workflow_dispatch, so mcloud deploy --rebuild can
	// start it.
	Dispatch bool
}

// File is the workflow's file name, which the GitHub API takes for it.
func (w *deployWorkflow) File() string { return filepath.Base(w.Path) }

// findDeployWorkflow finds the workflow under root/.github/workflows whose
// jobs call run-deploy. Exactly one must.
func findDeployWorkflow(root string) (*deployWorkflow, error) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var found []*deployWorkflow
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (filepath.Ext(name) != ".yaml" && filepath.Ext(name) != ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var w struct {
			// YAML 1.2: `on` is a string here, where YAML 1.1 parsers
			// (sigs.k8s.io/yaml) read it as true.
			On   any `yaml:"on"`
			Jobs map[string]struct {
				Uses string `yaml:"uses"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &w); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(".github", "workflows", name), err)
		}
		for _, j := range w.Jobs {
			if strings.HasPrefix(j.Uses, runDeployRef) {
				found = append(found, &deployWorkflow{
					Path:     ".github/workflows/" + name,
					Dispatch: hasEvent(w.On, "workflow_dispatch"),
				})
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no workflow in .github/workflows calls maxcloud's run-deploy (%s...); mcloud init writes one, deploy.yaml", runDeployRef)
	case 1:
		return found[0], nil
	default:
		paths := make([]string, len(found))
		for i, w := range found {
			paths[i] = w.Path
		}
		sort.Strings(paths)
		return nil, fmt.Errorf("several workflows call maxcloud's run-deploy (%s): keep one", strings.Join(paths, ", "))
	}
}

// hasEvent: a workflow's on: (a string, a list or a map) includes event.
func hasEvent(on any, event string) bool {
	switch v := on.(type) {
	case string:
		return v == event
	case []any:
		for _, e := range v {
			if e == event {
				return true
			}
		}
	case map[string]any:
		_, ok := v[event]
		return ok
	}
	return false
}

// repoRoot is the top of the git checkout mcloud runs in, else the current
// directory.
func repoRoot() string {
	if root, err := git("rev-parse", "--show-toplevel"); err == nil && root != "" {
		return root
	}
	return "."
}

// deployRun picks wf's run among a commit's runs (newest first): the one a
// push started, or with dispatched, the one workflow_dispatch did.
func deployRun(runs []github.Run, wf *deployWorkflow, dispatched bool) *github.Run {
	for i := range runs {
		r := runs[i]
		// GitHub may append the ref (@refs/heads/main) to a run's path.
		path, _, _ := strings.Cut(r.Path, "@")
		if path == wf.Path && (!dispatched || r.Event == "workflow_dispatch") {
			return &r
		}
	}
	return nil
}

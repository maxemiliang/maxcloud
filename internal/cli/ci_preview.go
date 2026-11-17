package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// PreviewLabel is the pull request label that asks for a preview.
const PreviewLabel = "preview"

// previewBranch is where a pull request's preview manifests go: a branch
// of its own, one commit with only deploy/k8s, force-pushed on every build.
// The app's preview entry (made by the maxcloud controller) points Flux
// there. Never the pull request's own branch: no commits in its history,
// nothing to merge.
func previewBranch(n int) string { return "maxcloud/preview/" + strconv.Itoa(n) }

// environmentManifests is an environment's manifests directory, on its own
// branch: deploy/env/<env> (the maxcloud controller points its entry
// there), so merging between an environment's branch and the app's never
// conflicts on deploy commits.
func environmentManifests(env string) string { return "deploy/env/" + env }

// pullRequestEvent is what ciPreview reads of GitHub's event payload.
type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"pull_request"`
}

// previewDecision is what a pull request's run does.
type previewDecision int

const (
	previewSkip previewDecision = iota
	previewBuild
	previewRemove
)

// decidePreview says what a pull request's run does, and why not when it
// does nothing. Only a pull request from a branch of this repo, labelled
// preview, of an app with previews on, is built: a fork's code never runs
// with this repo's credentials (pull_request gives a fork's run none, and
// pull_request_target, which would, is refused).
func decidePreview(event string, ev *pullRequestEvent, p *project) (previewDecision, string, error) {
	if event == "pull_request_target" {
		return previewSkip, "", errors.New("previews build on pull_request, not pull_request_target: that runs with this repo's credentials for any fork's pull request")
	}
	if ev.Number <= 0 {
		return previewSkip, "", errors.New("the event has no pull request number")
	}
	if ev.Action == "closed" {
		return previewRemove, "", nil
	}
	head := ev.PullRequest.Head.Repo
	if head == nil || head.FullName != ev.PullRequest.Base.Repo.FullName {
		return previewSkip, "pull requests from forks get no preview", nil
	}
	if !slices.ContainsFunc(ev.PullRequest.Labels, func(l struct {
		Name string `json:"name"`
	}) bool {
		return l.Name == PreviewLabel
	}) {
		return previewSkip, "no preview: the pull request isn't labelled " + PreviewLabel, nil
	}
	if p == nil || p.Previews == nil || !p.Previews.Enabled {
		return previewSkip, "no preview: previews: isn't on in " + ProjectFile, nil
	}
	return previewBuild, "", nil
}

func readPullRequestEvent() (*pullRequestEvent, error) {
	path := os.Getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return nil, errors.New("GITHUB_EVENT_PATH isn't set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ev pullRequestEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("the event payload: %w", err)
	}
	return &ev, nil
}

// ciPreview is the deploy workflow on a pull request: build the preview
// and push its manifests to maxcloud/preview/<n>, which Flux applies in
// <app>--pr-<n>; or, when the pull request closes, delete that branch (the
// maxcloud controller removes the preview itself).
func (g *globals) ciPreview(ctx context.Context, o ciOptions, event string) error {
	ev, err := readPullRequestEvent()
	if err != nil {
		return err
	}
	p, perr := loadProject()
	if perr != nil {
		p = nil
	}
	what, why, err := decidePreview(event, ev, p)
	if err != nil {
		return err
	}
	branch := previewBranch(ev.Number)
	switch what {
	case previewSkip:
		fmt.Fprintln(g.errOut, "::notice title=No preview::"+why)
		return nil
	case previewRemove:
		if _, err := git("push", "origin", "--delete", "refs/heads/"+branch); err != nil {
			fmt.Fprintf(g.out, "No branch %s to remove\n", branch)
		} else {
			fmt.Fprintf(g.out, "Removed %s; the preview goes with the pull request\n", branch)
		}
		return nil
	}
	if os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" {
		return errors.New("the maxcloud registry takes this repo's GitHub OIDC token: grant `id-token: write` in the repo's deploy workflow (mcloud init writes it)")
	}
	app := p.App
	if o.app != "" && o.app != app {
		return fmt.Errorf("the app input says %s, %s says %s", o.app, ProjectFile, app)
	}
	env := registry.PullRequestPrefix + strconv.Itoa(ev.Number)
	target := registry.EnvironmentName(app, env)
	dir, err := ciContext(o.context)
	if err != nil {
		return err
	}
	sha := ev.PullRequest.Head.SHA
	if sha == "" {
		sha = os.Getenv("GITHUB_SHA")
	}
	reg := imageRegistry()
	repo := previewImageRepo(app)
	d, err := newDockerEnv(reg, "")
	if err != nil {
		return err
	}
	defer d.close()
	// Its own cache: a pull request's build never writes the cache the
	// app's own builds read.
	ref := repo + ":" + env + "-" + sha[:min(7, len(sha))]
	fmt.Fprintf(g.out, "Building the preview of #%d, %s ...\n", ev.Number, ref)
	if err := g.buildImage(ctx, d, buildSpec{dir: dir, ref: ref, cache: repo + ":build-cache", ci: true}); err != nil {
		return err
	}
	digest, err := g.imageDigest(ctx, d, ref)
	if err != nil {
		return err
	}
	image := repo + "@" + digest

	work, err := os.MkdirTemp("", "maxcloud-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "deploy", "k8s")
	// The app's manifests as the pull request has them, then its
	// functions' (Flux applies them in the preview's namespace).
	if _, err := os.Stat(o.manifests); err == nil {
		if err := copyDir(o.manifests, out); err != nil {
			return err
		}
	}
	if o.functions {
		fmt.Fprintln(g.out, "Asking the image for its functions ...")
		desc, err := g.describeImage(ctx, d, image)
		if err != nil {
			return err
		}
		if _, err := functions.WriteDir(out, desc, functions.Options{
			Image: o.image, App: target, Env: p.envFor("", true), Environment: env, Preview: true,
		}); err != nil {
			return err
		}
	}
	pin := exec.CommandContext(ctx, "kustomize", "edit", "set", "image", o.image+"="+image)
	pin.Dir = out
	if b, err := pin.CombinedOutput(); err != nil {
		return fmt.Errorf("kustomize edit set image: %w: %s", err, b)
	}

	commit, err := commitTree(work, "deploy", fmt.Sprintf("preview: #%d at %s\n\nImage %s", ev.Number, sha[:min(7, len(sha))], image))
	if err != nil {
		return err
	}
	// Pushed with GITHUB_TOKEN, so it starts no run.
	if _, err := git("push", "--force", "origin", commit+":refs/heads/"+branch); err != nil {
		return err
	}
	fmt.Fprintf(g.out, "Preview pushed to %s: Flux deploys it as %s (https://<function>--%s.makiya.dev/)\n", branch, target, target)
	return nil
}

// commitTree makes a commit with no parent holding only dir's files from
// work (a directory outside the checkout), without touching the checkout
// or its index: its own index file and work tree. Returns the commit.
func commitTree(work, dir, msg string) (string, error) {
	env := append(os.Environ(),
		"GIT_INDEX_FILE="+filepath.Join(work, ".maxcloud-index"),
		"GIT_AUTHOR_NAME=github-actions[bot]", "GIT_COMMITTER_NAME=github-actions[bot]",
		"GIT_AUTHOR_EMAIL=41898282+github-actions[bot]@users.noreply.github.com",
		"GIT_COMMITTER_EMAIL=41898282+github-actions[bot]@users.noreply.github.com")
	gitDir, err := git("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	if _, err := gitWith(env, "-C", work, "--git-dir", gitDir, "--work-tree", ".", "add", "--force", dir); err != nil {
		return "", err
	}
	tree, err := gitWith(env, "write-tree")
	if err != nil {
		return "", err
	}
	return gitWith(env, "commit-tree", tree, "-m", msg)
}

func gitWith(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// copyDir copies the files under src to dst.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	})
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

type ciOptions struct {
	ci, functions                  bool
	context, manifests, image, app string
}

// ciBuild is the deploy workflow: build and push as the repo, write the
// functions' manifests, pin the image by digest, commit and push. Flux
// applies the commit.
func (g *globals) ciBuild(ctx context.Context, o ciOptions) error {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return errors.New("--ci is for the deploy workflow in GitHub Actions; run mcloud deploy here")
	}
	if os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" {
		return errors.New("the maxcloud registry takes this repo's GitHub OIDC token: grant `id-token: write` in the repo's deploy workflow (mcloud init writes it)")
	}
	app := o.app
	if app == "" {
		p, err := loadProject()
		if err != nil {
			return fmt.Errorf("no app name: set app: in maxcloud.yaml, or the app input (%w)", err)
		}
		app = p.App
	}
	dir, err := ciContext(o.context)
	if err != nil {
		return err
	}
	sha, branch := os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_REF_NAME")
	if sha == "" || branch == "" {
		return errors.New("GITHUB_SHA and GITHUB_REF_NAME aren't set")
	}
	registry := imageRegistry()
	repo := registry + "/" + app + "/" + app
	d, err := newDockerEnv(registry, "")
	if err != nil {
		return err
	}
	defer d.close()

	ref := repo + ":" + sha
	fmt.Fprintf(g.out, "Building %s ...\n", ref)
	if err := g.buildImage(ctx, d, buildSpec{dir: dir, ref: ref, cache: repo + ":build-cache", ci: true}); err != nil {
		return err
	}
	digest, err := g.imageDigest(ctx, d, ref)
	if err != nil {
		return err
	}
	image := repo + "@" + digest

	// Start from the branch as it is now: an earlier run may have pushed its
	// deploy commit since this one checked out.
	if _, err := git("fetch", "origin", branch); err != nil {
		return err
	}
	if _, err := git("checkout", "-B", branch, "origin/"+branch); err != nil {
		return err
	}

	// Ask the image, not the source, so the manifests match what was built.
	if o.functions {
		fmt.Fprintln(g.out, "Asking the image for its functions ...")
		desc, err := g.describeImage(ctx, d, image)
		if err != nil {
			return err
		}
		env, err := manifestEnv(app)
		if err != nil {
			return err
		}
		changed, err := functions.WriteDir(o.manifests, desc, functions.Options{Image: o.image, App: app, Env: env})
		if err != nil {
			return err
		}
		for _, f := range desc.Functions {
			fmt.Fprintf(g.out, "  %-9s %s\n", f.Kind, f.Name)
		}
		for _, p := range changed {
			fmt.Fprintf(g.out, "  wrote %s\n", p)
		}
		if _, err := os.Stat(filepath.Join(o.manifests, functions.LegacyDatabaseFile)); desc.NeedsDatabase() && err != nil {
			// Shown on the workflow run's summary page.
			fmt.Fprintln(g.errOut, "::notice title=Queue functions need a database::"+functions.NeedsDatabaseHint(app))
		}
		if w := imageMigrationsWarning(desc); w != "" {
			fmt.Fprintln(g.errOut, "::warning title=Migrations the image won't apply::"+w)
		}
	}

	// Pin the image; kustomize is on GitHub's runners.
	pin := exec.CommandContext(ctx, "kustomize", "edit", "set", "image", o.image+"="+image)
	pin.Dir = o.manifests
	if out, err := pin.CombinedOutput(); err != nil {
		return fmt.Errorf("kustomize edit set image: %w: %s", err, out)
	}

	// Pushed with GITHUB_TOKEN, so this commit doesn't start another run.
	for _, args := range [][]string{
		{"config", "user.name", "github-actions[bot]"},
		{"config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com"},
		{"add", o.manifests},
	} {
		if _, err := git(args...); err != nil {
			return err
		}
	}
	if err := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet").Run(); err == nil {
		fmt.Fprintln(g.out, "Image unchanged, nothing to deploy.")
		return nil
	}
	if _, err := git("commit", "-m", "deploy: "+sha[:min(7, len(sha))], "-m", "Image "+image); err != nil {
		return err
	}
	if _, err := git("push", "origin", "HEAD:"+branch); err != nil {
		return err
	}
	fmt.Fprintf(g.out, "Deploy commit pushed: %s\n", image)
	return nil
}

// ciContext is the directory the deploy workflow builds, relative to the
// current directory (the repo's root): --context if given, else source: in
// maxcloud.yaml, else ".".
func ciContext(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	p, err := loadProject()
	if err != nil {
		return ".", nil // no maxcloud.yaml (the app input names the app)
	}
	dir, err := p.sourceDir()
	if err != nil {
		return "", err
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		return dir, nil
	}
	return filepath.ToSlash(rel), nil
}

// buildPush builds the app here and pushes it: to the maxcloud registry
// with an hour-long token of the app's push ServiceAccount (whoever may
// manage the app's namespace may push its images), or to image: in
// maxcloud.yaml with your own login. Returns repo@digest and, with
// describe, the image's functions.
func (g *globals) buildPush(ctx context.Context, k *kube.Client, p *project, describe bool) (string, *functions.Description, error) {
	registry := imageRegistry()
	repo := p.Image
	if repo == "" {
		repo = registry + "/" + p.App + "/" + p.App
	}
	token := ""
	if hostOf(repo) == registry {
		t, _, err := k.RegistryPushToken(ctx, p.App, registry, time.Hour)
		if err != nil {
			return "", nil, err
		}
		token = t
	}
	src, err := p.sourceDir()
	if err != nil {
		return "", nil, err
	}
	d, err := newDockerEnv(registry, token)
	if err != nil {
		return "", nil, err
	}
	defer d.close()
	u := g.UI()
	ref := repo + ":direct-" + time.Now().UTC().Format("20060102-150405")
	step := u.StepTail("Building "+ref, 6)
	if err := g.buildImage(ctx, d, buildSpec{dir: src, ref: ref, cache: repo + ":build-cache", log: step.Writer()}); err != nil {
		step.Fail("Build failed")
		return "", nil, err
	}
	digest, err := g.imageDigest(ctx, d, ref)
	if err != nil {
		step.Fail("Build failed")
		return "", nil, err
	}
	image := repo + "@" + digest
	step.Done("Built %s", u.Accent(repo)+u.Dim("@"+shortDigest(digest)))
	if !describe {
		return image, nil, nil
	}
	step = u.Step("Asking the image for its functions")
	desc, err := g.describeImage(ctx, d, image)
	if err != nil {
		step.Fail("")
		return image, nil, err
	}
	step.Done("Found %d functions", len(desc.Functions))
	return image, desc, nil
}

// shortDigest is sha256:0123456789ab.
func shortDigest(d string) string {
	if len(d) > len("sha256:")+12 {
		return d[:len("sha256:")+12]
	}
	return d
}

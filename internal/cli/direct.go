package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

// The Paketo builder direct deploys build with; keep it the same as
// run-deploy.yaml's.
// renovate: datasource=docker depName=paketobuildpacks/ubuntu-noble-builder
const paketoBuilderVersion = "0.0.213"

// deployDirect builds the working tree, pushes the image, and applies the
// functions' manifests to the cluster itself: no git, no Flux.
func (g *globals) deployDirect(ctx context.Context, k *kube.Client, p *project) error {
	name := p.App
	st, err := k.AppStatus(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case st.Entry == nil:
		return fmt.Errorf("%s isn't registered; register it for direct deploys with `mcloud apps create %s --deploy direct`", name, name)
	case !st.Entry.Direct():
		return fmt.Errorf("%s deploys from git (Flux would undo a direct deploy); switch it with `mcloud apps set %s --deploy direct`", name, name)
	case st.Applied:
		return fmt.Errorf("%s still has a Flux deploy on the cluster; wait a minute for the registry to remove it", name)
	}
	if ok, err := k.NamespaceExists(ctx, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("namespace %s doesn't exist yet; Flux creates it within a minute of registering", name)
	}

	repo := p.Image
	if repo == "" {
		if r := originRepo(); r != "" {
			repo = "ghcr.io/" + strings.ToLower(r)
		} else {
			return fmt.Errorf("no image repository: set image: (e.g. ghcr.io/<you>/%s) in %s", name, ProjectFile)
		}
	}
	ref := repo + ":direct-" + time.Now().UTC().Format("20060102-150405")

	fmt.Fprintf(g.out, "Building %s ...\n", ref)
	if err := g.build(ctx, p.Dir, ref); err != nil {
		return err
	}
	digest, err := output(ctx, "docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest.Digest}}")
	if err != nil {
		return fmt.Errorf("reading the pushed image's digest: %w", err)
	}
	image := repo + "@" + strings.Trim(digest, `"`)

	fmt.Fprintln(g.out, "Asking the image for its functions ...")
	out, err := output(ctx, "docker", "run", "--rm", "--pull", "missing", "-e", "MAXCLOUD_DESCRIBE=1", image)
	if err != nil {
		return fmt.Errorf("describing the image: %w", err)
	}
	d, err := functions.Parse([]byte(out))
	if err != nil {
		return err
	}

	hasDB, err := k.Exists(ctx, kube.GVRCNPGCluster, name, functions.DatabaseName)
	if err != nil {
		return err
	}
	if d.NeedsDatabase() && !hasDB {
		db, err := functions.DatabaseObjects()
		if err != nil {
			return err
		}
		fmt.Fprintln(g.out, "Creating the app's database (the queue needs one) ...")
		if err := k.Apply(ctx, name, db); err != nil {
			return err
		}
		hasDB = true
	}
	objs := functions.Objects(d, functions.Options{
		Image:    image,
		Database: hasDB,
		Labels:   map[string]string{kube.DirectLabel: kube.FieldManager},
	})
	for _, f := range d.Functions {
		fmt.Fprintf(g.out, "  %-9s %s\n", f.Kind, f.Name)
	}
	if err := k.Apply(ctx, name, objs); err != nil {
		return err
	}
	pruned, err := k.Prune(ctx, name, objs)
	for _, p := range pruned {
		fmt.Fprintf(g.out, "  removed   %s (no longer in the app)\n", p)
	}
	if err != nil {
		return err
	}
	return g.followServices(ctx, k, name)
}

// build makes the image from dir and pushes it: with the Dockerfile if
// there is one, else with buildpacks (pack).
func (g *globals) build(ctx context.Context, dir, ref string) error {
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = g.errOut, g.errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", name, args[0], err)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		// The cluster's nodes are amd64.
		if err := run("docker", "build", "--platform", "linux/amd64", "-t", ref, "."); err != nil {
			return err
		}
		return run("docker", "push", ref)
	}
	if _, err := exec.LookPath("pack"); err != nil {
		return errors.New("building without a Dockerfile needs pack (Cloud Native Buildpacks): mise use -g pack, or see buildpacks.io")
	}
	args := []string{"build", ref, "--path", ".", "--builder", "paketobuildpacks/ubuntu-noble-builder:" + paketoBuilderVersion, "--publish"}
	if extra := os.Getenv("MCLOUD_PACK_FLAGS"); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	return run("pack", args...)
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

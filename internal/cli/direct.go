package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// deployDirect builds the working tree, pushes the image, and applies the
// functions' manifests to the cluster itself: no git, no Flux.
func (g *globals) deployDirect(ctx context.Context, k *kube.Client, p *project, started time.Time) error {
	return g.deployDirectTo(ctx, k, p, directTarget{}, started)
}

// directTarget is where a direct deploy goes: the app itself (zero), or one
// of its environments or previews, <app>--<env>.
type directTarget struct {
	env     string
	preview bool
}

// deployDirectTo is deployDirect into t: the app's image (built and pushed
// as the app), applied in the environment's namespace with the
// environment's variables.
func (g *globals) deployDirectTo(ctx context.Context, k *kube.Client, p *project, t directTarget, started time.Time) error {
	name := p.App
	if t.env != "" {
		name = registry.EnvironmentName(p.App, t.env)
		if t.preview && p.Image == "" {
			// Previews' images have a repository of their own, which the
			// registry keeps for two weeks only.
			q := *p
			q.Image = previewImageRepo(p.App)
			p = &q
		}
	}
	st, err := k.AppStatus(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case st.Entry == nil && t.env != "":
		return fmt.Errorf("%s has no registry entry yet: the maxcloud controller makes it from %s's entry (environments: or previews: in %s, then mcloud deploy); mcloud status %s shows why if it doesn't", name, p.App, ProjectFile, p.App)
	case st.Entry == nil:
		return fmt.Errorf("%s isn't registered; register it for direct deploys with `mcloud apps create %s --deploy direct`", name, name)
	case !st.Entry.Direct():
		return fmt.Errorf("%s deploys from git (Flux would undo a direct deploy); switch it with deploy: direct in maxcloud.yaml (or `mcloud apps set %s --deploy direct`)", name, name)
	case st.Applied:
		return fmt.Errorf("%s still has a Flux deploy on the cluster; wait a minute for the registry to remove it", name)
	}
	if ok, err := k.NamespaceExists(ctx, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("namespace %s doesn't exist yet; Flux creates it within a minute of registering", name)
	}

	src, err := p.sourceDir()
	if err != nil {
		return err
	}
	where, err := buildWhere(ctx, src, g.buildMode)
	if err != nil {
		return err
	}
	build := g.buildPush
	if where == "cluster" {
		build = func(ctx context.Context, k *kube.Client, p *project, _ bool) (string, *functions.Description, error) {
			return g.clusterBuildPush(ctx, k, p)
		}
	}
	image, d, err := build(ctx, k, p, true)
	if err != nil {
		return err
	}

	if d.NeedsDatabase() {
		db, err := k.GetDatabase(ctx, name)
		if err != nil {
			return err
		}
		hasCluster, err := k.Exists(ctx, kube.GVRCNPGCluster, name, functions.DatabaseName)
		if err != nil {
			return err
		}
		if db == nil && !hasCluster {
			g.UI().Note("%s", functions.NeedsDatabaseHint(name))
		}
	}
	if w := imageMigrationsWarning(d); w != "" {
		g.UI().Warn("%s", w)
	}
	if w := functions.ConnectionsWarning(d); w != "" {
		g.UI().Warn("%s", w)
	}
	env := p.Env
	if t.env != "" {
		env = p.envFor(t.env, t.preview)
	}
	objs := functions.Objects(d, functions.Options{
		Image:       image,
		App:         name,
		Env:         env,
		Labels:      map[string]string{kube.DirectLabel: kube.FieldManager},
		Environment: t.env,
		Preview:     t.preview,
	})
	u := g.UI()
	step := u.Step(fmt.Sprintf("Applying %d functions", len(d.Functions)))
	if err := k.Apply(ctx, name, objs); err != nil {
		step.Fail("")
		return err
	}
	pruned, err := k.Prune(ctx, name, objs)
	if err != nil {
		step.Fail("")
		return err
	}
	step.Done("Applied %d functions", len(d.Functions))
	for _, f := range d.Functions {
		u.Hint("%-9s %s", f.Kind, f.Name)
	}
	for _, p := range pruned {
		u.Hint("removed   %s (no longer in the app)", p)
	}
	g.recordDirectDeploy(ctx, k, name, directDeployRecord(image, d), objs)
	return g.followServices(ctx, k, name, started)
}

// previewImageRepo is where previews' images go: <app>/<app>--preview in
// the maxcloud registry, which keeps them two weeks
// (platform/infrastructure/controllers/registry/config.json). No app
// name has "--", so no app's own images match.
func previewImageRepo(app string) string {
	return imageRegistry() + "/" + app + "/" + app + "--preview"
}

// imageRegistry is the maxcloud registry's host.
func imageRegistry() string {
	return envOr("MCLOUD_IMAGE_REGISTRY", "registry.gronblom.dev")
}

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

// deployDirect builds the working tree, pushes the image, and applies the
// functions' manifests to the cluster itself: no git, no Flux.
func (g *globals) deployDirect(ctx context.Context, k *kube.Client, p *project, started time.Time) error {
	name := p.App
	st, err := k.AppStatus(ctx, name)
	if err != nil {
		return err
	}
	switch {
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

	where, err := buildWhere(ctx, p.Dir, g.buildMode)
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
	objs := functions.Objects(d, functions.Options{
		Image:  image,
		App:    name,
		Labels: map[string]string{kube.DirectLabel: kube.FieldManager},
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
	return g.followServices(ctx, k, name, started)
}

// imageRegistry is the maxcloud registry's host.
func imageRegistry() string {
	return envOr("MCLOUD_IMAGE_REGISTRY", "registry.gronblom.dev")
}

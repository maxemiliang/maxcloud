package cli

import (
	"context"
	"fmt"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

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
			fmt.Fprintln(g.errOut, "note: "+functions.NeedsDatabaseHint(name))
		}
	}
	objs := functions.Objects(d, functions.Options{
		Image:  image,
		App:    name,
		Labels: map[string]string{kube.DirectLabel: kube.FieldManager},
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

// imageRegistry is the maxcloud registry's host.
func imageRegistry() string {
	return envOr("MCLOUD_IMAGE_REGISTRY", "registry.gronblom.dev")
}

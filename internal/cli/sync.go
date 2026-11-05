package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// entry is the app's registry entry as maxcloud.yaml describes it, on top of
// base (the current entry, or nil for a new app): what maxcloud.yaml says
// wins, what it leaves out stays.
func (p *project) entry(base *registry.Entry) registry.Entry {
	e := registry.Entry{Name: p.App}
	if base != nil {
		e = *base
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	if p.Deploy != "" {
		e.Deploy = p.Deploy
		if p.Deploy == "gitops" {
			e.Deploy = ""
		}
	}
	if e.Direct() {
		e.Repo, e.Path, e.Branch = "", "", ""
	} else {
		set(&e.Repo, p.Repo)
		if e.Repo == "" {
			e.Repo = originRepo()
		}
		set(&e.Path, p.Path)
		set(&e.Branch, p.Branch)
	}
	if q := p.Quota; q != nil {
		set(&e.CPU, q.CPU)
		set(&e.Memory, q.Memory)
		set(&e.Pods, q.Pods)
	}
	if p.Buckets != nil {
		e.Buckets = strings.Join(p.Buckets, ",")
	}
	if d := p.DB; d != nil {
		if d.Enabled {
			e.DB = "true"
			set(&e.DBSize, d.Size)
			set(&e.DBInstances, d.Instances)
		} else {
			e.DB, e.DBSize, e.DBInstances = "", "", ""
		}
	}
	return e
}

// syncApp registers the app from maxcloud.yaml, or brings its entry in line
// with it. Returns whether the app was new.
func (g *globals) syncApp(ctx context.Context, k *kube.Client, p *project) (bool, error) {
	cur, err := k.GetRegistryEntry(ctx, p.App)
	if err != nil {
		return false, err
	}
	if cur != nil && cur.InGit {
		fmt.Fprintf(g.errOut, "note: %s is registered in the homelab repo; maxcloud.yaml's settings there win over this file\n", p.App)
		return false, nil
	}
	var base *registry.Entry
	if cur != nil {
		base = cur.Entry
	}
	e := p.entry(base)
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%s: %w", ProjectFile, err)
	}
	if cur == nil {
		if err := g.checkNamespace(ctx, e.Name); err != nil {
			return false, err
		}
		fmt.Fprintf(g.out, "Registering %s (%s) ...\n", e.Name, source(&e, g.registry))
		if err := k.PutRegistryEntry(ctx, &e); err != nil {
			return false, err
		}
		if !e.Direct() {
			fmt.Fprintf(g.out, "For Flux to read %s, the homelab's Flux GitHub App needs access to it (GitHub, Settings, Applications, the Flux app, Repository access).\n", e.Repo)
		}
		return true, nil
	}
	diff := diffEntries(base, &e)
	if len(diff) == 0 {
		return false, nil
	}
	fmt.Fprintf(g.out, "Updating %s from %s: %s\n", e.Name, ProjectFile, strings.Join(diff, ", "))
	if base.Direct() != e.Direct() {
		return false, fmt.Errorf("%s changes how %s deploys (gitops/direct): do that on purpose with `mcloud apps set %s --deploy ...`", ProjectFile, e.Name, e.Name)
	}
	return false, k.PutRegistryEntry(ctx, &e)
}

package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
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
	if p.Domains != nil {
		e.Domains = registry.FormatDomains(p.Domains)
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

// target is the registry entry a deploy brings base to: p.entry, with
// --direct (direct) switching an app whose maxcloud.yaml doesn't say how it
// deploys. How the app deploys is maxcloud.yaml's deploy: when it has one,
// else the entry's current mode (as mcloud apps set --deploy left it).
func (p *project) target(base *registry.Entry, direct bool) (registry.Entry, error) {
	q := *p
	if direct {
		if p.Deploy != "" && p.Deploy != registry.DeployDirect {
			return registry.Entry{}, fmt.Errorf("--direct, but %s says deploy: %s: change it there", ProjectFile, p.Deploy)
		}
		q.Deploy = registry.DeployDirect
	}
	e := q.entry(base)
	if err := e.Validate(); err != nil {
		return registry.Entry{}, fmt.Errorf("%s: %w", ProjectFile, err)
	}
	return e, nil
}

// syncedFields are the registry keys maxcloud.yaml declares, which it owns
// (the same rule as the controller's AppSync).
func (p *project) syncedFields() []string {
	var out []string
	if p.Deploy != "" {
		out = append(out, "deploy")
	}
	if q := p.Quota; q != nil {
		if q.CPU != "" {
			out = append(out, "cpu")
		}
		if q.Memory != "" {
			out = append(out, "memory")
		}
		if q.Pods != "" {
			out = append(out, "pods")
		}
	}
	if p.Buckets != nil {
		out = append(out, "buckets")
	}
	if p.DB != nil {
		out = append(out, "db", "dbInstances", "dbSize")
	}
	if p.Domains != nil {
		out = append(out, "domains")
	}
	sort.Strings(out)
	return out
}

// syncApp registers the app from maxcloud.yaml, or brings its entry in line
// with it (see target; direct is --direct). Returns the entry the app now
// has, which says how it deploys, and whether the app was new. Switching
// between gitops and direct asks first, unless yes.
func (g *globals) syncApp(ctx context.Context, k *kube.Client, p *project, direct, yes bool) (*registry.Entry, bool, error) {
	cur, err := k.GetRegistryEntry(ctx, p.App)
	if err != nil {
		return nil, false, err
	}
	if cur != nil && cur.InGit {
		g.UI().Note("%s is registered in the homelab repo; the settings there win over %s", p.App, ProjectFile)
		return cur.Entry, false, nil
	}
	var base *registry.Entry
	if cur != nil {
		base = cur.Entry
	}
	e, err := p.target(base, direct)
	if err != nil {
		return nil, false, err
	}
	if cur == nil {
		if err := g.checkNamespace(ctx, e.Name); err != nil {
			return nil, false, err
		}
		if err := k.PutRegistryEntry(ctx, &e); err != nil {
			return nil, false, err
		}
		g.UI().Success("Registered %s %s", g.UI().Bold(e.Name), g.UI().Dim("("+source(&e)+")"))
		if err := k.SetSyncedFields(ctx, e.Name, p.syncedFields()); err != nil {
			return nil, false, err
		}
		if !e.Direct() {
			g.UI().Hint("For Flux to read %s, the homelab's Flux GitHub App needs access to it (GitHub, Settings, Applications, the Flux app, Repository access).", e.Repo)
		}
		return &e, true, nil
	}
	if fields := p.syncedFields(); !slices.Equal(fields, cur.SyncedFields) {
		if err := k.SetSyncedFields(ctx, e.Name, fields); err != nil {
			return nil, false, err
		}
	}
	diff := diffEntries(base, &e)
	if len(diff) == 0 {
		return &e, false, nil
	}
	g.UI().Printf("%s Updating %s from %s", g.UI().Accent("↻"), g.UI().Bold(e.Name), ProjectFile)
	for _, d := range diff {
		g.UI().Hint("%s", d)
	}
	if base.Direct() != e.Direct() {
		g.explainSwitch(&e)
		if !yes {
			ok, err := g.confirm("Switch it?")
			if err != nil || !ok {
				return nil, false, orAborted(err)
			}
		}
	}
	return &e, false, k.PutRegistryEntry(ctx, &e)
}

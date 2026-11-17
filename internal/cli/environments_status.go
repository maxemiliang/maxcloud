package cli

import (
	"context"

	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// printEnvironments adds an app's environments and previews to mcloud
// status: which exist, where they deploy from and whether their deploy
// applied. For an environment itself, which app it belongs to. Nothing
// for an app without any.
func (g *globals) printEnvironments(ctx context.Context, k *kube.Client, name string) {
	u := g.UI()
	if app, env, ok := registry.SplitEnvironment(name); ok {
		u.Println()
		u.Note("%s is environment %s of %s (mcloud status %s shows them all)", name, env, app, app)
		return
	}
	envs, problem, err := k.Environments(ctx, name)
	if err != nil {
		u.Warn("environments: %v", err)
		return
	}
	previews, _ := k.Previews(ctx, name)
	expires := map[string]string{}
	for _, p := range previews {
		if !p.ExpiresAt.IsZero() {
			expires[p.Name] = "expires " + p.ExpiresAt.Local().Format("Mon 2 Jan 15:04")
		}
	}
	if len(envs) > 0 {
		var rows [][]string
		for _, e := range envs {
			from := e.Branch
			if from == "" {
				from = "direct"
			}
			deploy := u.Mark(e.Deploy.Status, e.Applied)
			if e.Applied && !e.Deploy.Ready() && e.Deploy.Message != "" {
				deploy += " " + truncate(e.Deploy.Message, 50)
			}
			rows = append(rows, []string{u.Accent(e.Env), e.Kind, from, deploy, u.Dim(expires[e.Env])})
		}
		u.Println()
		u.Table([]string{"Environment", "Kind", "From", "Deployed", ""}, rows)
		u.Hint("--env <name> reaches one: mcloud status --env %s, mcloud logs --env %s", envs[0].Env, envs[0].Env)
	}
	for _, p := range previews {
		if p.Phase == "Waiting" {
			u.Warn("preview %s waits: %s", p.Name, p.Message)
		}
	}
	if problem != "" {
		u.Warn("environments: %s", problem)
	}
}

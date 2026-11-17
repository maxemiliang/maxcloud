package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

func previewCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "preview",
		Short: "Short-lived deploys of a working tree or a pull request, each at its own URLs",
		Long: `A preview is a short-lived copy of the app in an environment of its own,
<app>--<name>: its own namespace, URLs (https://<function>--<app>--<name>.makiya.dev/),
secrets, empty buckets and, with previews.db, an empty database. Never the
app's secrets or data. It's removed, data included, when it expires
(previews.ttl after its last deploy, 72h by default), when it's deleted,
or for a pull request when the pull request closes.

previews: in maxcloud.yaml turns them on (then mcloud deploy, or a push).
A GitOps app then gets one per open pull request labelled preview, which
its deploy workflow builds; mcloud preview deploy builds this working
tree instead, for any app. Only the app's owners may make one.

The other commands reach a preview with --env <name>:
mcloud logs --env pr-42, mcloud secrets set --env alice KEY.`,
	}
	c.AddCommand(previewDeployCmd(g), previewListCmd(g), previewDeleteCmd(g))
	return c
}

func previewDeployCmd(g *globals) *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "deploy [name]",
		Short: "Build this working tree and deploy it as a preview",
		Long: `Build this working tree (uncommitted changes included), as a direct deploy
does, and deploy it as the preview name (default: your login), in its own
environment <app>--<name>. Deploying again replaces it and moves its
expiry on by the app's previews.ttl.

The image goes to <app>/<app>--preview in the maxcloud registry, which
keeps previews' images for two weeks. Scheduled functions don't run on
their schedule in a preview (mcloud run --env <name> runs one), and HTTP
functions and queue workers scale to one instance at most.`,
		Example: `  mcloud preview deploy             # as your login: https://web--shop--ann.makiya.dev/
  mcloud preview deploy checkout    # https://web--shop--checkout.makiya.dev/`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			started := time.Now()
			p, err := loadProject()
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			} else if name, err = defaultPreviewName(ctx, k); err != nil {
				return err
			}
			if err := checkPreviewName(p.App, name); err != nil {
				return err
			}
			cur, err := k.GetRegistryEntry(ctx, p.App)
			if err != nil {
				return err
			}
			if cur == nil {
				return fmt.Errorf("%s isn't registered: mcloud deploy registers it", p.App)
			}
			if cur.Entry.Previews != "true" {
				return fmt.Errorf("previews are off for %s: add previews: true to %s, then mcloud deploy (or push)", p.App, ProjectFile)
			}
			ttl, err := previewTTL(cur.Entry)
			if err != nil {
				return err
			}
			u := g.UI()
			expires := time.Now().Add(ttl)
			if err := k.PutPreview(ctx, p.App, name, expires); err != nil {
				return fmt.Errorf("preview %s: %w", name, err)
			}
			env := registry.EnvironmentName(p.App, name)
			step := u.Step("Waiting for the preview's environment " + env)
			if err := waitPreview(ctx, k, p.App, name); err != nil {
				step.Fail("")
				return err
			}
			if err := g.waitNamespace(ctx, env, 3*time.Minute); err != nil {
				step.Fail("")
				return err
			}
			step.Done("Environment %s", u.Accent(env))
			if err := g.deployDirectTo(ctx, k, p, directTarget{env: name, preview: true}, started); err != nil {
				return err
			}
			u.Hint("expires %s, %s from now (deploying again moves it on); mcloud preview delete %s removes it now",
				expires.Local().Format("Mon 15:04"), ttl.Round(time.Hour), name)
			return nil
		},
	}
	c.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "give up after this long")
	c.Flags().StringVar(&g.buildMode, "build", "auto", "where to build: local (docker, pack), cluster (buildpacks in the app's namespace), or auto")
	return c
}

func previewListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list [app]",
		Short: "List the app's previews: pull requests' and working trees'",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := appOnly(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ps, err := k.Previews(cmd.Context(), app)
			if err != nil {
				return err
			}
			u := g.UI()
			if len(ps) == 0 {
				u.Note("%s has no previews", app)
				return nil
			}
			var rows [][]string
			for _, p := range ps {
				from := "working tree"
				if p.PullRequest > 0 {
					from = fmt.Sprintf("pull request #%d", p.PullRequest)
				}
				expires := "-"
				if !p.ExpiresAt.IsZero() {
					expires = p.ExpiresAt.Local().Format("Mon 2 Jan 15:04")
				}
				rows = append(rows, []string{u.Accent(p.Name), from, u.Mark(map[bool]string{true: "True", false: "False"}[p.Phase == "Active"], p.Phase != ""), p.Phase, expires, truncate(p.Message, 70)})
			}
			u.Table([]string{"Name", "From", "", "Phase", "Expires", "Message"}, rows)
			return nil
		},
	}
}

func previewDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name> [app]",
		Short: "Remove a preview now, with its data",
		Long: `Remove a preview now: its environment, namespace, buckets and database go
with it. A pull request's preview comes back with the pull request's next
push; take the label preview off the pull request to stop that.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := appOnly(args[1:])
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			if err := k.DeletePreview(cmd.Context(), app, args[0]); err != nil {
				return fmt.Errorf("preview %s of %s: %w", args[0], app, err)
			}
			g.UI().Success("Removing preview %s (%s)", args[0], registry.EnvironmentName(app, args[0]))
			return nil
		},
	}
}

// appOnly is the app a command acts on (argument, $MCLOUD_APP,
// maxcloud.yaml), without --env: preview commands name previews
// themselves.
func appOnly(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if v := os.Getenv("MCLOUD_APP"); v != "" {
		return v, nil
	}
	p, err := loadProject()
	if err != nil {
		return "", err
	}
	return p.App, nil
}

func checkPreviewName(app, name string) error {
	if err := registry.ValidateEnvironment(app, name); err != nil {
		return fmt.Errorf("preview %s", strings.TrimPrefix(err.Error(), "environment "))
	}
	if strings.HasPrefix(name, registry.PullRequestPrefix) {
		return fmt.Errorf("preview %s: names starting with %s are pull requests'", name, registry.PullRequestPrefix)
	}
	return nil
}

// defaultPreviewName is the login's username as a preview name: lowercase
// letters and digits, single dashes, at most 12.
func defaultPreviewName(ctx context.Context, k *kube.Client) (string, error) {
	who, err := k.Whoami(ctx)
	if err != nil {
		return "", err
	}
	name := previewNameFrom(strings.TrimPrefix(who, kube.AutheliaPrefix))
	if name == "" {
		return "", fmt.Errorf("can't make a preview name from %s: name it (mcloud preview deploy <name>)", who)
	}
	return name, nil
}

func previewNameFrom(user string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(user) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if b.Len() == 0 && r >= '0' && r <= '9' {
				continue
			}
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= registry.MaxEnvironmentName {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func previewTTL(e *registry.Entry) (time.Duration, error) {
	if e.PreviewTTL == "" {
		return 72 * time.Hour, nil
	}
	return time.ParseDuration(e.PreviewTTL)
}

// waitPreview waits until the controller has given the preview its
// environment, or says why not.
func waitPreview(ctx context.Context, k *kube.Client, app, name string) error {
	for {
		p, err := k.GetPreview(ctx, app, name)
		if err != nil {
			return err
		}
		if p != nil {
			switch p.Phase {
			case "Active":
				return nil
			case "Waiting", "Expired":
				return fmt.Errorf("preview %s: %s", name, p.Message)
			}
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("preview %s: the maxcloud controller didn't make its environment: %w", name, err)
		}
	}
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

func deployCmd(g *globals) *cobra.Command {
	var rebuild, noPush, direct, yes bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "deploy",
		Short: "Push this repo and follow the build and the rollout",
		Long: `Deploy the app in the current repo: push the branch, then follow the
deploy workflow (build, manifests, deploy commit), Flux applying the deploy
commit, and the app's services becoming ready.

A deploy is a push: committing and pushing yourself deploys just the same,
and mcloud deploy only follows along. Uncommitted changes aren't deployed.
With nothing new to push, --rebuild runs the workflow again (e.g. after a
change in the homelab platform).

The app's settings (quota, buckets, database, source) come from
maxcloud.yaml: the first deploy registers the app, later ones bring its
registry entry in line. No commit to the homelab repo.

A direct app (deploy: direct in maxcloud.yaml) is deployed without git:
mcloud builds the working tree, uncommitted changes included, pushes the
image to the maxcloud registry (registry.gronblom.dev/<app>/<app>) with a
short-lived token of the app's push ServiceAccount (nothing to log in to),
and applies the functions' manifests with your kubeconfig.

It builds with docker: the Dockerfile, or buildpacks (pack, or pack in a
container). Without docker, an app without a Dockerfile builds in the
cluster instead (--build cluster): a buildpacks pod in the app's
namespace, with the source streamed to it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			p, err := loadProject()
			if err != nil {
				return err
			}
			name := p.App
			if direct {
				p.Deploy = "direct"
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			// The app's registry entry comes from maxcloud.yaml: registered on
			// the first deploy, kept in line after that. No homelab commit.
			created, err := g.syncApp(ctx, k, p, yes)
			if err != nil {
				return err
			}
			if p.Deploy == "direct" {
				if created {
					if err := g.waitNamespace(ctx, name, 3*time.Minute); err != nil {
						return err
					}
				}
				return g.deployDirect(ctx, k, p)
			}
			repo := originRepo()
			if repo == "" {
				return errors.New("this repo has no GitHub remote (origin)")
			}
			branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return err
			}
			if dirty, _ := git("status", "--porcelain"); dirty != "" {
				fmt.Fprintln(g.errOut, "warning: uncommitted changes aren't part of this deploy")
			}
			gh, err := g.github()
			if err != nil {
				return err
			}

			if !noPush {
				fmt.Fprintf(g.out, "Pushing %s to %s ...\n", branch, repo)
				push := exec.CommandContext(ctx, "git", "push", "origin", "HEAD:"+branch)
				push.Stdout, push.Stderr = g.errOut, g.errOut
				if err := push.Run(); err != nil {
					return fmt.Errorf("git push: %w", err)
				}
			}
			sha, err := git("rev-parse", "HEAD")
			if err != nil {
				return err
			}
			if rebuild {
				if err := gh.Dispatch(ctx, github.Repo(repo), "deploy.yaml", branch); err != nil {
					return fmt.Errorf("starting the deploy workflow: %w", err)
				}
			}

			run, err := g.followRun(ctx, gh, github.Repo(repo), sha, rebuild)
			if err != nil {
				return err
			}
			if run == nil {
				fmt.Fprintln(g.out, "No deploy workflow ran for this commit (only deploy/ changed?); following Flux.")
			}

			target, err := gh.Head(ctx, github.Repo(repo), branch)
			if err != nil {
				return err
			}
			if err := g.followFlux(ctx, k, name, target); err != nil {
				return err
			}
			return g.followServices(ctx, k, name)
		},
	}
	f := c.Flags()
	f.BoolVar(&rebuild, "rebuild", false, "run the deploy workflow again even with nothing new to push")
	f.BoolVar(&noPush, "no-push", false, "don't push; follow the deploy of what's already pushed")
	f.BoolVar(&direct, "direct", false, "deploy a direct app: build here and apply, without git (default for deploy: direct in maxcloud.yaml)")
	f.DurationVar(&timeout, "timeout", 15*time.Minute, "give up following after this long")
	f.StringVar(&g.buildMode, "build", "auto", "direct apps: where to build: local (docker, pack), cluster (buildpacks in the app's namespace, no docker needed), or auto (local if docker is installed)")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask before switching between gitops and direct (deploy: in maxcloud.yaml)")
	return c
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// followRun waits for the deploy workflow run of sha to finish. Returns nil
// if none starts within a minute (a push that only changed deploy/).
func (g *globals) followRun(ctx context.Context, gh *github.Client, repo github.Repo, sha string, dispatched bool) (*github.Run, error) {
	var run *github.Run
	appeared := time.Now()
	for run == nil {
		runs, err := gh.RunsFor(ctx, repo, sha)
		if err != nil {
			return nil, err
		}
		for i := range runs {
			r := runs[i]
			if r.Name == "deploy" && (!dispatched || r.Event == "workflow_dispatch") {
				run = &r
				break
			}
		}
		if run != nil {
			break
		}
		if time.Since(appeared) > time.Minute {
			return nil, nil
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(g.out, "Build: %s\n", run.HTMLURL)
	last := ""
	for {
		if run.Status != last {
			fmt.Fprintf(g.out, "  %s\n", strings.ReplaceAll(run.Status, "_", " "))
			last = run.Status
		}
		if run.Status == "completed" {
			if run.Conclusion != "success" {
				return nil, fmt.Errorf("the deploy workflow ended %s: %s", run.Conclusion, run.HTMLURL)
			}
			return run, nil
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return nil, err
		}
		r, err := gh.Run(ctx, repo, run.ID)
		if err != nil {
			return nil, err
		}
		run = r
	}
}

// followFlux waits until the app's Kustomization has applied target.
func (g *globals) followFlux(ctx context.Context, k *kube.Client, name, target string) error {
	fmt.Fprintf(g.out, "Flux: applying %s ...\n", target[:7])
	if err := k.RequestReconcile(ctx, name); err != nil {
		fmt.Fprintf(g.errOut, "warning: can't ask Flux to fetch now (%v); it will within a minute\n", err)
	}
	last := ""
	askedForAccess := false
	for {
		st, err := k.AppStatus(ctx, name)
		if err != nil {
			return err
		}
		// Flux can't read the repo: usually its GitHub App hasn't been given
		// it yet. Open the page for that once and keep waiting; Flux tries
		// again every minute.
		if st.Source != nil && st.Source.NoRepoAccess() && !askedForAccess {
			askedForAccess = true
			u := repoAccessURL(st)
			fmt.Fprintf(g.out, "  Flux can't read %s: %s\n", st.Entry.Repo, firstLine(st.Source.Message))
			fmt.Fprintf(g.out, "  Give the homelab's Flux GitHub App access to it (Repository access, add %s):\n    %s\n  Waiting for Flux to fetch it ...\n", st.Entry.Repo, u)
			openBrowser(u)
		}
		if !st.Applied {
			return fmt.Errorf("%s has no Flux deploy on the cluster; register it with `mcloud apps create`", name)
		}
		if strings.HasSuffix(st.Revision, target) && st.Deploy.Ready() {
			fmt.Fprintln(g.out, "  applied")
			return nil
		}
		if msg := firstLine(st.Deploy.Message); msg != last && !strings.Contains(msg, target) {
			fmt.Fprintf(g.out, "  %s\n", msg)
			last = msg
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return fmt.Errorf("Flux hasn't applied %s yet: %s", target[:7], st.Deploy.Message)
		}
	}
}

// repoAccessURL is where Flux's GitHub App gets access to more repos: the
// page the registry names, else the account's list of installed apps.
func repoAccessURL(st *kube.Status) string {
	if st.SourceAccessURL != "" {
		return st.SourceAccessURL
	}
	return "https://github.com/settings/installations"
}

// followServices waits for every Knative service to have its newest revision
// ready, then prints the status.
func (g *globals) followServices(ctx context.Context, k *kube.Client, name string) error {
	for {
		st, err := k.AppStatus(ctx, name)
		if err != nil {
			return err
		}
		pending := false
		for _, s := range st.Services {
			if !s.Ready.Ready() {
				pending = true
				if s.Ready.Status == "False" {
					printStatus(g.out, st)
					return fmt.Errorf("service %s isn't ready: %s%s", s.Name, s.Ready.Message, pullHint(st))
				}
			}
		}
		if !pending {
			fmt.Fprintln(g.out)
			printStatus(g.out, st)
			return nil
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			printStatus(g.out, st)
			return errors.New("services not ready in time" + pullHint(st))
		}
	}
}

// pullHint explains an image the cluster can't pull.
func pullHint(st *kube.Status) string {
	for _, p := range st.Problems {
		if strings.Contains(p, "ImagePull") || strings.Contains(p, "ErrImagePull") {
			return "\n(the cluster can't pull the image: from the maxcloud registry an app pulls only its own images, registry.gronblom.dev/<app>/...; a private GHCR package needs making public in its settings on GitHub)"
		}
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

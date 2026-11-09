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
		Short: "Push this repo and follow the build and the rollout (direct apps: build here and apply)",
		Long: `Deploy the app in the current repo: push the branch, then follow the
deploy workflow (build, manifests, deploy commit), Flux applying the deploy
commit, and the app's services becoming ready.

A deploy is a push: committing and pushing yourself deploys just the same,
and mcloud deploy only follows along. Uncommitted changes aren't deployed.
With nothing new to push, --rebuild runs the workflow again (e.g. after a
change in the homelab platform).

The app's settings (quota, buckets, database, domains, deploy) come from
maxcloud.yaml: the first deploy registers the app, later ones bring its
registry entry in line. No commit to the platform's git.

How the app deploys is maxcloud.yaml's deploy: (gitops or direct) when it
has one. Without it, the app keeps the mode it has: gitops for a new app,
or what --direct or mcloud apps set --deploy last switched it to.

A direct app is deployed without git:
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
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			started := time.Now()
			// The app's registry entry comes from maxcloud.yaml: registered on
			// the first deploy, kept in line after that. No platform commit.
			// How it deploys is the entry's: maxcloud.yaml's deploy: when it
			// has one, else what the app was last switched to.
			e, created, err := g.syncApp(ctx, k, p, direct, yes)
			if err != nil {
				return err
			}
			if e.Direct() {
				if created {
					if err := g.waitNamespace(ctx, name, 3*time.Minute); err != nil {
						return err
					}
				}
				return g.deployDirect(ctx, k, p, started)
			}
			repo := originRepo()
			if repo == "" {
				return errors.New("this repo has no GitHub remote (origin)")
			}
			branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return err
			}
			u := g.UI()
			if dirty, _ := git("status", "--porcelain"); dirty != "" {
				u.Warn("uncommitted changes aren't part of this deploy")
			}
			gh, err := g.github()
			if err != nil {
				return err
			}

			if !noPush {
				step := u.StepTail(fmt.Sprintf("Pushing %s to %s", branch, repo), 3)
				push := exec.CommandContext(ctx, "git", "push", "origin", "HEAD:"+branch)
				push.Stdout, push.Stderr = step.Writer(), step.Writer()
				if err := push.Run(); err != nil {
					step.Fail("git push failed")
					return fmt.Errorf("git push: %w", err)
				}
				step.Done("Pushed %s to %s", branch, u.Accent(repo))
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
				u.Note("no deploy workflow ran for this commit (only deploy/ changed?); following Flux")
			}

			target, err := gh.Head(ctx, github.Repo(repo), branch)
			if err != nil {
				return err
			}
			if err := g.followFlux(ctx, k, name, target); err != nil {
				return err
			}
			return g.followServices(ctx, k, name, started)
		},
	}
	f := c.Flags()
	f.BoolVar(&rebuild, "rebuild", false, "run the deploy workflow again even with nothing new to push")
	f.BoolVar(&noPush, "no-push", false, "don't push; follow the deploy of what's already pushed")
	f.BoolVar(&direct, "direct", false, "switch the app to direct (asks first) and deploy it: build here and apply, without git (refused if maxcloud.yaml says deploy: gitops)")
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
	u := g.UI()
	step := u.Step("Waiting for the deploy workflow")
	var run *github.Run
	appeared := time.Now()
	for run == nil {
		runs, err := gh.RunsFor(ctx, repo, sha)
		if err != nil {
			step.Fail("")
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
			step.Skip("No deploy workflow ran")
			return nil, nil
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			step.Fail("")
			return nil, err
		}
	}
	step.Skip("Deploy workflow started")
	u.Hint("%s", u.Link(run.HTMLURL))
	step = u.Step("Building")
	for {
		step.Status("%s", strings.ReplaceAll(run.Status, "_", " "))
		if run.Status == "completed" {
			if run.Conclusion != "success" {
				step.Fail("The deploy workflow ended %s", run.Conclusion)
				return nil, fmt.Errorf("the deploy workflow ended %s: %s", run.Conclusion, run.HTMLURL)
			}
			step.Done("Built and committed the deploy")
			return run, nil
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			step.Fail("")
			return nil, err
		}
		r, err := gh.Run(ctx, repo, run.ID)
		if err != nil {
			step.Fail("")
			return nil, err
		}
		run = r
	}
}

// followFlux waits until the app's Kustomization has applied target.
func (g *globals) followFlux(ctx context.Context, k *kube.Client, name, target string) error {
	u := g.UI()
	if err := k.RequestReconcile(ctx, name); err != nil {
		u.Warn("can't ask Flux to fetch now (%v); it will within a minute", err)
	}
	step := u.Step("Flux applying " + target[:7])
	askedForAccess := false
	for {
		st, err := k.AppStatus(ctx, name)
		if err != nil {
			step.Fail("")
			return err
		}
		// Flux can't read the repo: usually its GitHub App hasn't been given
		// it yet. Open the page for that once and keep waiting; Flux tries
		// again every minute.
		if st.Source != nil && st.Source.NoRepoAccess() && !askedForAccess {
			askedForAccess = true
			url := repoAccessURL(st)
			u.Warn("Flux can't read %s: %s", st.Entry.Repo, firstLine(st.Source.Message))
			u.Hint("Give the homelab's Flux GitHub App access to it (Repository access, add %s):", st.Entry.Repo)
			u.Hint("%s", u.Link(url))
			step.Status("waiting for Flux to fetch it")
			openBrowser(url)
		}
		if !st.Applied {
			step.Fail("")
			return fmt.Errorf("%s has no Flux deploy on the cluster; register it with `mcloud apps create`", name)
		}
		if strings.HasSuffix(st.Revision, target) && st.Deploy.Ready() {
			step.Done("Flux applied %s", target[:7])
			return nil
		}
		if msg := firstLine(st.Deploy.Message); !strings.Contains(msg, target) {
			step.Status("%s", msg)
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			step.Fail("Flux hasn't applied %s yet", target[:7])
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
func (g *globals) followServices(ctx context.Context, k *kube.Client, name string, started time.Time) error {
	u := g.UI()
	step := u.Step("Rolling out")
	for {
		st, err := k.AppStatus(ctx, name)
		if err != nil {
			step.Fail("")
			return err
		}
		ready := 0
		for _, s := range st.Services {
			if s.Ready.Ready() {
				ready++
				continue
			}
			if s.Ready.Status == "False" {
				step.Fail("Service %s isn't ready", s.Name)
				u.Println()
				printStatus(u, st)
				return fmt.Errorf("service %s isn't ready: %s%s", s.Name, s.Ready.Message, pullHint(st))
			}
		}
		if ready == len(st.Services) {
			step.Done("Rolled out")
			u.Println()
			u.Printf("%s %s %s", u.Green("●"), u.Bold(name), u.Dim("deployed in "+time.Since(started).Round(time.Second).String()))
			u.Println()
			printStatus(u, st)
			return nil
		}
		step.Status("%d/%d services ready", ready, len(st.Services))
		if err := sleep(ctx, 2*time.Second); err != nil {
			step.Fail("Services not ready in time")
			u.Println()
			printStatus(u, st)
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

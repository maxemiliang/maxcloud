package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kustomize"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

func appsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "apps",
		Short: "Register, change and remove apps",
	}
	c.AddCommand(appsListCmd(g), appsCreateCmd(g), appsSetCmd(g), appsDeleteCmd(g))
	return c
}

func appsListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered apps and whether their last deploy applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := g.kube()
			if err != nil {
				return err
			}
			apps, err := k.Apps(cmd.Context())
			if err != nil {
				return err
			}
			if len(apps) == 0 {
				fmt.Fprintln(g.out, "No apps registered. Add one with `mcloud apps create`.")
				return nil
			}
			tw := tabwriter.NewWriter(g.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSOURCE\tDEPLOYED\tREVISION\tMESSAGE")
			for _, a := range apps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, source(a.Entry, g.registry), mark(a.Deploy.Status, a.Applied), shortRev(a.Revision), truncate(a.Deploy.Message, 60))
			}
			return tw.Flush()
		},
	}
}

func source(e *registry.Entry, registryRepo string) string {
	if e == nil {
		return "?"
	}
	if e.Direct() {
		return "direct (mcloud deploy)"
	}
	repo, p := e.Repo, e.Path
	if repo == "" {
		repo = registryRepo
	}
	if p == "" {
		p = registry.DefaultPath
	}
	if e.Branch != "" && e.Branch != registry.DefaultBranch {
		return repo + "@" + e.Branch + ":" + p
	}
	return repo + ":" + p
}

// entryFlags binds the registry fields to flags.
func entryFlags(f *pflag.FlagSet, e *registry.Entry) {
	f.StringVar(&e.Repo, "repo", "", "the app's GitHub repo, <owner>/<repo>; leave out for an app kept in the registry repo")
	f.StringVar(&e.Path, "path", "", "directory of the app's manifests in that repo (default deploy/k8s)")
	f.StringVar(&e.Branch, "branch", "", "branch to deploy (default main)")
	f.StringVar(&e.CPU, "cpu", "", "quota: CPU the app's pods may request in total (default 200m)")
	f.StringVar(&e.Memory, "memory", "", "quota: memory the app's pods may request in total (default 1Gi)")
	f.StringVar(&e.Pods, "pods", "", "quota: number of pods (default 20)")
	f.StringVar(&e.Deploy, "deploy", "", "gitops (default: Flux applies the app's manifests from git) or direct (mcloud deploy applies them)")
	f.StringVar(&e.Buckets, "buckets", "", "S3 buckets, comma-separated; name:public for one anyone can read (uploads,assets:public). Removing one from the list keeps its objects")
	f.StringVar(&e.DB, "db", "", "a Postgres database run by the maxcloud controller, backed up and restored by itself (--db, or --db=false to remove it: its backups are kept)")
	f.Lookup("db").NoOptDefVal = "true"
	f.StringVar(&e.DBInstances, "db-instances", "", "database instances: 1 (default), or 2 for a replica on the other node")
	f.StringVar(&e.DBSize, "db-size", "", "database disk per instance (default 1Gi; can grow, not shrink)")
}

type commitFlags struct {
	yes, dryRun, wait bool
	timeout           time.Duration
}

func (cf *commitFlags) bind(f *pflag.FlagSet, wait bool) {
	f.BoolVarP(&cf.yes, "yes", "y", false, "don't ask before committing")
	f.BoolVar(&cf.dryRun, "dry-run", false, "show the change, commit nothing")
	if wait {
		f.BoolVar(&cf.wait, "wait", false, "after committing, wait until the app's deploy has applied")
		f.DurationVar(&cf.timeout, "timeout", 5*time.Minute, "how long --wait waits")
	}
}

func (g *globals) entryPath(name string) string { return path.Join(g.dir, registry.FilePath(name)) }
func (g *globals) kustPath() string             { return path.Join(g.dir, "kustomization.yaml") }

func appsCreateCmd(g *globals) *cobra.Command {
	var e registry.Entry
	var cf commitFlags
	c := &cobra.Command{
		Use:   "create NAME",
		Short: "Register an app: its namespace, quota and deploys from its repo",
		Long: `Register an app on maxcloud. This commits one file to the registry (and
lists it there), and Flux then creates the app's namespace with its quota and
guardrails, and starts deploying the app's manifests from its repo.

The name becomes the namespace, and public services get
https://<service>-<name>.gronblom.dev.`,
		Example: `  mcloud apps create shop --repo maxemiliang/shop
  mcloud apps create shop --repo maxemiliang/shop --cpu 500m --memory 2Gi`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e.Name = args[0]
			return g.createApp(cmd.Context(), e, cf)
		},
	}
	entryFlags(c.Flags(), &e)
	cf.bind(c.Flags(), true)
	return c
}

// createApp registers e: one commit to the registry, after showing it and
// asking (unless cf says otherwise).
func (g *globals) createApp(ctx context.Context, e registry.Entry, cf commitFlags) error {
	if err := e.Validate(); err != nil {
		return err
	}
	gh, err := g.github()
	if err != nil {
		return err
	}
	if err := g.checkNamespace(ctx, e.Name); err != nil {
		return err
	}
	if e.Repo != "" && !e.Direct() {
		g.checkRepo(ctx, gh, &e)
	}

	content := e.Render()
	fmt.Fprintf(g.out, "Adds %s to %s (%s):\n\n%s\n", g.entryPath(e.Name), g.registry, g.branch, indent(string(content)))
	fmt.Fprintf(g.out, "and lists it in %s.\n\n", g.kustPath())
	if cf.dryRun {
		return nil
	}
	if !cf.yes {
		ok, err := g.confirm(fmt.Sprintf("Commit to %s, which deploys it?", g.registry))
		if err != nil || !ok {
			return orAborted(err)
		}
	}
	sha, err := gh.Commit(ctx, github.Repo(g.registry), g.branch, fmt.Sprintf("feat(maxcloud): add app %s\n\nRegistered with mcloud: %s.", e.Name, source(&e, g.registry)),
		func(read github.Reader) ([]github.Change, error) {
			if _, err := read(g.entryPath(e.Name)); err == nil {
				return nil, fmt.Errorf("%s is already registered; change it with `mcloud apps set`", e.Name)
			} else if !errors.Is(err, github.ErrNotFound) {
				return nil, err
			}
			k, err := read(g.kustPath())
			if err != nil {
				return nil, err
			}
			nk, _, err := kustomize.AddResource(string(k), registry.FilePath(e.Name))
			if err != nil {
				return nil, err
			}
			return []github.Change{{Path: g.entryPath(e.Name), Content: content}, {Path: g.kustPath(), Content: []byte(nk)}}, nil
		})
	if err != nil {
		return err
	}
	fmt.Fprintf(g.out, "Committed https://github.com/%s/commit/%s\n", g.registry, sha)
	if e.Repo != "" && !e.Direct() {
		fmt.Fprintf(g.out, `
For Flux to read %[1]s, the homelab's Flux GitHub App needs access to it:
GitHub, Settings, Applications, the Flux app, Configure, Repository access.
`, e.Repo)
	}
	if cf.wait {
		if e.Direct() {
			return g.waitNamespace(ctx, e.Name, cf.timeout)
		}
		return g.waitDeployed(ctx, e.Name, cf.timeout)
	}
	fmt.Fprintf(g.out, "\nFollow it with `mcloud status %s`.\n", e.Name)
	return nil
}

func appsSetCmd(g *globals) *cobra.Command {
	var flags registry.Entry
	var cf commitFlags
	c := &cobra.Command{
		Use:   "set NAME",
		Short: "Change an app's quota or source",
		Long: `Change fields of a registered app. Only the flags you pass change; pass an
empty value (--cpu "") to go back to the default.`,
		Example: `  mcloud apps set shop --cpu 500m --memory 2Gi
  mcloud apps set shop --buckets uploads,assets:public
  mcloud apps set shop --db --db-size 2Gi`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			changed := map[string]*string{}
			for flag, v := range map[string]*string{"repo": &flags.Repo, "path": &flags.Path, "branch": &flags.Branch, "cpu": &flags.CPU, "memory": &flags.Memory, "pods": &flags.Pods, "deploy": &flags.Deploy, "buckets": &flags.Buckets, "db": &flags.DB, "db-instances": &flags.DBInstances, "db-size": &flags.DBSize} {
				if cmd.Flags().Changed(flag) {
					changed[flag] = v
				}
			}
			if len(changed) == 0 {
				return errors.New("nothing to change: pass --cpu, --memory, --pods, --repo, --path, --branch, --deploy, --buckets or --db")
			}
			apply := func(e *registry.Entry) {
				fields := map[string]*string{"repo": &e.Repo, "path": &e.Path, "branch": &e.Branch, "cpu": &e.CPU, "memory": &e.Memory, "pods": &e.Pods, "deploy": &e.Deploy, "buckets": &e.Buckets, "db": &e.DB, "db-instances": &e.DBInstances, "db-size": &e.DBSize}
				for flag, v := range changed {
					*fields[flag] = *v
				}
			}
			gh, err := g.github()
			if err != nil {
				return err
			}
			cur, err := gh.File(ctx, github.Repo(g.registry), g.branch, g.entryPath(name))
			if errors.Is(err, github.ErrNotFound) {
				return fmt.Errorf("%s isn't registered (no %s)", name, g.entryPath(name))
			} else if err != nil {
				return err
			}
			old, err := registry.Parse(cur)
			if err != nil {
				return fmt.Errorf("%s: %w", g.entryPath(name), err)
			}
			e := *old
			apply(&e)
			if err := e.Validate(); err != nil {
				return err
			}
			if e == *old {
				fmt.Fprintln(g.out, "Nothing changes.")
				return nil
			}
			fmt.Fprintf(g.out, "Changes %s in %s:\n", name, g.registry)
			for _, d := range diffEntries(old, &e) {
				fmt.Fprintf(g.out, "  %s\n", d)
			}
			if old.Direct() != e.Direct() {
				fmt.Fprintf(g.out, "\nSwitching how %s deploys replaces its workloads: ", name)
				if e.Direct() {
					fmt.Fprintln(g.out, "Flux removes everything it applied (databases included, if they came from git), and the app is down until `mcloud deploy` applies it again.")
				} else {
					fmt.Fprintln(g.out, "Flux applies the app from git and takes over objects of the same name; delete any that mcloud applied and git doesn't have.")
				}
			}
			fmt.Fprintln(g.out)
			if cf.dryRun {
				return nil
			}
			if !cf.yes {
				ok, err := g.confirm("Commit, which deploys the change?")
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			sha, err := gh.Commit(ctx, github.Repo(g.registry), g.branch, fmt.Sprintf("chore(maxcloud): %s: %s", name, strings.Join(diffEntries(old, &e), ", ")),
				func(read github.Reader) ([]github.Change, error) {
					cur, err := read(g.entryPath(name))
					if err != nil {
						return nil, err
					}
					e, err := registry.Parse(cur)
					if err != nil {
						return nil, err
					}
					apply(e)
					if err := e.Validate(); err != nil {
						return nil, err
					}
					return []github.Change{{Path: g.entryPath(name), Content: e.Render()}}, nil
				})
			if err != nil {
				return err
			}
			if sha == "" {
				fmt.Fprintln(g.out, "Nothing changed.")
				return nil
			}
			fmt.Fprintf(g.out, "Committed https://github.com/%s/commit/%s\n", g.registry, sha)
			return nil
		},
	}
	entryFlags(c.Flags(), &flags)
	cf.bind(c.Flags(), false)
	return c
}

func diffEntries(a, b *registry.Entry) []string {
	var out []string
	show := func(v, def string) string {
		if v == "" {
			return def + " (default)"
		}
		return v
	}
	for _, f := range []struct{ name, a, b, def string }{
		{"repo", a.Repo, b.Repo, "the registry repo"},
		{"path", a.Path, b.Path, registry.DefaultPath},
		{"branch", a.Branch, b.Branch, registry.DefaultBranch},
		{"cpu", a.CPU, b.CPU, registry.DefaultCPU},
		{"memory", a.Memory, b.Memory, registry.DefaultMemory},
		{"pods", a.Pods, b.Pods, registry.DefaultPods},
		{"deploy", a.Deploy, b.Deploy, "gitops"},
		{"buckets", a.Buckets, b.Buckets, "none"},
		{"db", a.DB, b.DB, "none"},
		{"db-instances", a.DBInstances, b.DBInstances, "1"},
		{"db-size", a.DBSize, b.DBSize, "1Gi"},
	} {
		if f.a != f.b {
			out = append(out, fmt.Sprintf("%s %s -> %s", f.name, show(f.a, f.def), show(f.b, f.def)))
		}
	}
	return out
}

func appsDeleteCmd(g *globals) *cobra.Command {
	var cf commitFlags
	c := &cobra.Command{
		Use:   "delete NAME",
		Short: "Remove an app, its namespace and all its data",
		Long: `Remove an app from the registry. Flux then deletes everything the
registry made for it, including its namespace, and with it every database and
volume in it. There is no undo; restore a database from its backup if it had
one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			gh, err := g.github()
			if err != nil {
				return err
			}
			if _, err := gh.File(ctx, github.Repo(g.registry), g.branch, g.entryPath(name)); errors.Is(err, github.ErrNotFound) {
				return fmt.Errorf("%s isn't registered (no %s)", name, g.entryPath(name))
			} else if err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Removes %s from %s. Flux then deletes namespace %s and everything in it", g.entryPath(name), g.registry, name)
			if k, err := g.kube(); err == nil {
				if st, err := k.AppStatus(ctx, name); err == nil && len(st.Databases) > 0 {
					var dbs []string
					for _, d := range st.Databases {
						dbs = append(dbs, d.Name)
					}
					fmt.Fprintf(g.out, ", including the database(s) %s", strings.Join(dbs, ", "))
				}
			}
			fmt.Fprint(g.out, ".\n\n")
			if cf.dryRun {
				return nil
			}
			if !cf.yes {
				ok, err := g.typeToConfirm(fmt.Sprintf("Type the app's name (%s) to delete it:", name), name)
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			sha, err := gh.Commit(ctx, github.Repo(g.registry), g.branch, fmt.Sprintf("chore(maxcloud): remove app %s", name),
				func(read github.Reader) ([]github.Change, error) {
					k, err := read(g.kustPath())
					if err != nil {
						return nil, err
					}
					nk, listed, err := kustomize.RemoveResource(string(k), registry.FilePath(name))
					if err != nil {
						return nil, err
					}
					var changes []github.Change
					if listed {
						changes = append(changes, github.Change{Path: g.kustPath(), Content: []byte(nk)})
					}
					if _, err := read(g.entryPath(name)); err == nil {
						changes = append(changes, github.Change{Path: g.entryPath(name)})
					} else if !errors.Is(err, github.ErrNotFound) {
						return nil, err
					}
					if len(changes) == 0 {
						return nil, fmt.Errorf("%s isn't registered any more", name)
					}
					return changes, nil
				})
			if err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Committed https://github.com/%s/commit/%s\n", g.registry, sha)
			return nil
		},
	}
	cf.bind(c.Flags(), false)
	return c
}

// checkNamespace refuses a name whose namespace exists but wasn't made by
// the registry: registering it would hand that namespace to the registry,
// and removing the app later would delete it.
func (g *globals) checkNamespace(ctx context.Context, name string) error {
	k, err := g.kube()
	if err != nil {
		fmt.Fprintf(g.errOut, "warning: can't check the cluster for an existing namespace %s: %v\n", name, err)
		return nil
	}
	ns, err := k.Core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil // not found, or unreachable: nothing to protect
	}
	if ns.Labels["resourceset.fluxcd.controlplane.io/name"] != "maxcloud-apps" {
		return fmt.Errorf("namespace %s already exists and isn't an app's; pick another name", name)
	}
	return nil
}

// checkRepo warns about a repo or manifests directory that isn't there yet.
// Not fatal: Flux retries until it is.
func (g *globals) checkRepo(ctx context.Context, gh *github.Client, e *registry.Entry) {
	ok, err := gh.RepoExists(ctx, github.Repo(e.Repo))
	switch {
	case err != nil:
		fmt.Fprintf(g.errOut, "warning: can't check %s: %v\n", e.Repo, err)
		return
	case !ok:
		fmt.Fprintf(g.errOut, "warning: %s doesn't exist, or your GitHub login can't see it\n", e.Repo)
		return
	}
	p, b := e.Path, e.Branch
	if p == "" {
		p = registry.DefaultPath
	}
	if b == "" {
		b = registry.DefaultBranch
	}
	if ok, err := gh.Exists(ctx, github.Repo(e.Repo), b, p); err == nil && !ok {
		fmt.Fprintf(g.errOut, "warning: %s has no %s on %s yet; Flux reports the app as failing until it does\n", e.Repo, p, b)
	}
}

func (g *globals) waitDeployed(ctx context.Context, name string, timeout time.Duration) error {
	k, err := g.kube()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(g.out, "\nWaiting for Flux to deploy %s ...\n", name)
	last := ""
	for {
		st, err := k.AppStatus(ctx, name)
		if err == nil {
			msg := "not registered on the cluster yet"
			if st.Applied {
				msg = st.Deploy.Message
				if msg == "" {
					msg = "waiting for the first reconcile"
				}
			}
			if msg != last {
				fmt.Fprintf(g.out, "  %s\n", msg)
				last = msg
			}
			if st.Applied && st.Deploy.Ready() {
				fmt.Fprintf(g.out, "Deployed. `mcloud status %s` shows its services.\n", name)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("not deployed after %s; see `mcloud status %s`", timeout, name)
		case <-time.After(3 * time.Second):
		}
	}
}

// waitNamespace waits for a direct app's namespace, which is all Flux
// creates for it.
func (g *globals) waitNamespace(ctx context.Context, name string, timeout time.Duration) error {
	k, err := g.kube()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(g.out, "\nWaiting for Flux to create namespace %s ...\n", name)
	for {
		if ok, err := k.NamespaceExists(ctx, name); err == nil && ok {
			fmt.Fprintf(g.out, "Ready for `mcloud deploy`.\n")
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no namespace %s after %s; see `mcloud apps list`", name, timeout)
		case <-time.After(3 * time.Second):
		}
	}
}

func orAborted(err error) error {
	if err != nil {
		return err
	}
	return errors.New("aborted")
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n    ")
}

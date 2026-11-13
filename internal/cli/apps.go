package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

func appsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "apps",
		Short: "Register, change and remove apps",
	}
	c.AddCommand(appsListCmd(g), appsCreateCmd(g), appsSetCmd(g), appsOwnersCmd(g), appsDeleteCmd(g))
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
				g.UI().Note("no apps registered: mcloud deploy in an app's repo registers it")
				return nil
			}
			u := g.UI()
			var rows [][]string
			for _, a := range apps {
				where := "cluster"
				if a.InGit {
					where = "platform git"
				}
				rows = append(rows, []string{u.Accent(a.Name), source(a.Entry), u.Dim(where), owners(a.Entry), u.Mark(a.Deploy.Status, a.Applied), u.Dim(shortRev(a.Revision)), truncate(a.Deploy.Message, 60)})
			}
			u.Table([]string{"Name", "Source", "Registered", "Owners", "Deployed", "Revision", "Message"}, rows)
			return nil
		},
	}
}

// owners is how apps list and status show an app's owners.
func owners(e *registry.Entry) string {
	if e == nil {
		return "?"
	}
	if o := e.OwnerList(); len(o) > 0 {
		return strings.Join(o, ", ")
	}
	return "admins"
}

func source(e *registry.Entry) string {
	if e == nil {
		return "?"
	}
	if e.Direct() {
		return "direct (mcloud deploy)"
	}
	repo, p := e.Repo, e.Path
	if repo == "" {
		repo = "the maxcloud repo"
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
	f.StringVar(&e.Domains, "domains", "", "custom hostnames, comma-separated <host>:<service>[:proxied] (paja.lankkupelit.fi:web:proxied); only those an admin allowed the app")
}

type confirmFlags struct {
	yes, dryRun, wait bool
	timeout           time.Duration
}

func (cf *confirmFlags) bind(f *pflag.FlagSet, wait bool) {
	f.BoolVarP(&cf.yes, "yes", "y", false, "don't ask before changing the registry")
	f.BoolVar(&cf.dryRun, "dry-run", false, "show the change, make none")
	if wait {
		f.BoolVar(&cf.wait, "wait", false, "then wait until the app's deploy has applied")
		f.DurationVar(&cf.timeout, "timeout", 5*time.Minute, "how long --wait waits")
	}
}

func appsCreateCmd(g *globals) *cobra.Command {
	var e registry.Entry
	var cf confirmFlags
	c := &cobra.Command{
		Use:   "create NAME",
		Short: "Register an app: its namespace, quota and deploys from its repo",
		Long: `Register an app on maxcloud. This adds the app's entry to the registry in
the cluster (with your kubeconfig, no commit anywhere), and the registry then
creates the app's namespace with its quota and guardrails, its buckets and
database, and starts deploying the app's manifests from its repo.

In an app's repo, mcloud deploy does this by itself from maxcloud.yaml.

You own the app you register (--owners adds the people you run it with):
owners are admin in its namespace, and only they and admins may change or
remove it. Change them later with mcloud apps owners.

The name becomes the namespace, and public services get
https://<service>--<name>.makiya.dev.`,
		Example: `  mcloud apps create shop --repo maxemiliang/shop
  mcloud apps create shop --repo maxemiliang/shop --cpu 500m --memory 2Gi --db
  mcloud apps create shop --repo maxemiliang/shop --owners alice,bob`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e.Name = args[0]
			return g.createApp(cmd.Context(), e, cf)
		},
	}
	entryFlags(c.Flags(), &e)
	c.Flags().StringVar(&e.Owners, "owners", "", "co-owners besides you, comma-separated Authelia usernames")
	cf.bind(c.Flags(), true)
	return c
}

// createApp registers e in the cluster, after showing it and asking
// (unless cf says otherwise).
func (g *globals) createApp(ctx context.Context, e registry.Entry, cf confirmFlags) error {
	if err := e.Validate(); err != nil {
		return err
	}
	k, err := g.kube()
	if err != nil {
		return err
	}
	if cur, err := k.GetRegistryEntry(ctx, e.Name); err != nil {
		return err
	} else if cur != nil {
		return fmt.Errorf("%s is already registered; change it with `mcloud apps set`", e.Name)
	}
	if err := g.checkNamespace(ctx, e.Name); err != nil {
		return err
	}
	if err := g.addOwner(ctx, k, &e); err != nil {
		return err
	}
	if e.Repo != "" && !e.Direct() {
		if gh, err := g.github(); err == nil {
			g.checkRepo(ctx, gh, &e)
		}
	}

	g.UI().Printf("Registers %s:\n\n%s\n", g.UI().Bold(e.Name), g.UI().Dim(indent(string(e.Render()))))
	if cf.dryRun {
		return nil
	}
	if !cf.yes {
		ok, err := g.confirm("Register it, which creates its namespace and deploys it?")
		if err != nil || !ok {
			return orAborted(err)
		}
	}
	if err := k.PutRegistryEntry(ctx, &e); err != nil {
		return err
	}
	g.UI().Success("Registered %s", g.UI().Bold(e.Name))
	if e.Repo != "" && !e.Direct() {
		g.UI().Hint("For Flux to read %s, the homelab's Flux GitHub App needs access to it:", e.Repo)
		g.UI().Hint("GitHub, Settings, Applications, the Flux app, Configure, Repository access.")
	}
	if cf.wait {
		if e.Direct() {
			return g.waitNamespace(ctx, e.Name, cf.timeout)
		}
		return g.waitDeployed(ctx, e.Name, cf.timeout)
	}
	g.UI().Hint("Follow it with `mcloud status %s`.", e.Name)
	return nil
}

func appsSetCmd(g *globals) *cobra.Command {
	var flags registry.Entry
	var cf confirmFlags
	c := &cobra.Command{
		Use:   "set NAME",
		Short: "Change a registered app by hand (apps with a maxcloud.yaml: edit that)",
		Long: `Change fields of a registered app. Only the flags you pass change; pass an
empty value (--cpu "") to go back to the default.

An app's maxcloud.yaml owns what it declares (deploy, quota, buckets, db,
domains): change those in the file. This refuses them, since the next push
or deploy would undo them. Use it for what the file leaves out, for apps
without one, and to switch --deploy between gitops and direct when the file
has no deploy:.`,
		Example: `  mcloud apps set shop --cpu 500m --memory 2Gi
  mcloud apps set shop --buckets uploads,assets:public
  mcloud apps set shop --db --db-size 2Gi`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			changed := map[string]*string{}
			for flag, v := range map[string]*string{"repo": &flags.Repo, "path": &flags.Path, "branch": &flags.Branch, "cpu": &flags.CPU, "memory": &flags.Memory, "pods": &flags.Pods, "deploy": &flags.Deploy, "buckets": &flags.Buckets, "db": &flags.DB, "db-instances": &flags.DBInstances, "db-size": &flags.DBSize, "domains": &flags.Domains} {
				if cmd.Flags().Changed(flag) {
					changed[flag] = v
				}
			}
			if len(changed) == 0 {
				return errors.New("nothing to change: pass --cpu, --memory, --pods, --repo, --path, --branch, --deploy, --buckets, --db, --db-size, --db-instances or --domains")
			}
			apply := func(e *registry.Entry) {
				fields := map[string]*string{"repo": &e.Repo, "path": &e.Path, "branch": &e.Branch, "cpu": &e.CPU, "memory": &e.Memory, "pods": &e.Pods, "deploy": &e.Deploy, "buckets": &e.Buckets, "db": &e.DB, "db-instances": &e.DBInstances, "db-size": &e.DBSize, "domains": &e.Domains}
				for flag, v := range changed {
					*fields[flag] = *v
				}
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			cur, err := k.GetRegistryEntry(ctx, name)
			if err != nil {
				return err
			}
			if cur == nil {
				return fmt.Errorf("%s isn't registered (mcloud apps list)", name)
			}
			if cur.InGit {
				return fmt.Errorf("%s is registered in git: change platform/apps/maxcloud/apps/%s.yaml in the maxcloud repo", name, name)
			}
			// What the app's maxcloud.yaml declares is its to change: a value
			// set here would be undone by the next push.
			var owned []string
			for flag := range changed {
				if key, ok := entryKeys[flag]; ok && slices.Contains(cur.SyncedFields, key) {
					owned = append(owned, "--"+flag)
				}
			}
			if len(owned) > 0 {
				sort.Strings(owned)
				return fmt.Errorf("%s's maxcloud.yaml sets %s: change it there and push (or mcloud deploy)", name, strings.Join(owned, ", "))
			}
			old := cur.Entry
			e := *old
			apply(&e)
			if err := e.Validate(); err != nil {
				return err
			}
			return g.changeApp(ctx, k, old, &e, cf)
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
		{"domains", a.Domains, b.Domains, "none"},
		{"owners", a.Owners, b.Owners, "none, admins only"},
	} {
		if f.a != f.b {
			out = append(out, fmt.Sprintf("%s %s -> %s", f.name, show(f.a, f.def), show(f.b, f.def)))
		}
	}
	return out
}

// changeApp shows what changes from old to e, asks (unless cf says
// otherwise), and writes e to the registry.
func (g *globals) changeApp(ctx context.Context, k *kube.Client, old, e *registry.Entry, cf confirmFlags) error {
	diff := diffEntries(old, e)
	if len(diff) == 0 {
		g.UI().Note("nothing changes")
		return nil
	}
	g.UI().Printf("Changes %s:", g.UI().Bold(e.Name))
	for _, d := range diff {
		g.UI().Printf("  %s", d)
	}
	if old.Direct() != e.Direct() {
		g.UI().Println()
		g.explainSwitch(e)
	}
	if old.DB == "true" && e.DB != "true" {
		g.UI().Println()
		g.UI().Warn("removing the database deletes it; its backups stay in Object Storage")
	}
	g.UI().Println()
	if cf.dryRun {
		return nil
	}
	if !cf.yes {
		ok, err := g.confirm("Apply the change?")
		if err != nil || !ok {
			return orAborted(err)
		}
	}
	if err := k.PutRegistryEntry(ctx, e); err != nil {
		return err
	}
	g.UI().Success("Changed %s", g.UI().Bold(e.Name))
	return nil
}

// explainSwitch says what switching between gitops and direct does.
func (g *globals) explainSwitch(e *registry.Entry) {
	what := "Flux applies the app from git and takes over objects of the same name; delete any that mcloud applied and git doesn't have."
	if e.Direct() {
		what = "Flux removes everything it applied from the app's repo, and the app is down until `mcloud deploy` applies it again."
	}
	g.UI().Warn("switching how %s deploys replaces its workloads: %s", e.Name, what)
}

// entryKeys maps mcloud apps set's flags to registry keys, for the fields
// an app's maxcloud.yaml can own.
var entryKeys = map[string]string{"deploy": "deploy", "cpu": "cpu", "memory": "memory", "pods": "pods", "buckets": "buckets", "db": "db", "db-size": "dbSize", "db-instances": "dbInstances", "domains": "domains"}

func appsDeleteCmd(g *globals) *cobra.Command {
	var cf confirmFlags
	c := &cobra.Command{
		Use:   "delete NAME",
		Short: "Remove an app, its namespace and all its data",
		Long: `Remove an app from the registry. Flux then deletes everything the
registry made for it, including its namespace, and with it every database and
volume in it. There is no undo. Only the app's owners (and admins) may.

Its buckets and database backups stay in Object Storage, but belong to this
registration: registering the name again starts without them, unless an
admin gives the new entry the old data.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			k, err := g.kube()
			if err != nil {
				return err
			}
			cur, err := k.GetRegistryEntry(ctx, name)
			if err != nil {
				return err
			}
			if cur == nil {
				return fmt.Errorf("%s isn't registered (mcloud apps list)", name)
			}
			if cur.InGit {
				return fmt.Errorf("%s is registered in git: remove platform/apps/maxcloud/apps/%s.yaml in the maxcloud repo", name, name)
			}
			msg := fmt.Sprintf("Removes %s from the registry, which then deletes namespace %s and everything in it", name, name)
			if st, err := k.AppStatus(ctx, name); err == nil && len(st.Databases) > 0 {
				var dbs []string
				for _, d := range st.Databases {
					dbs = append(dbs, d.Name)
				}
				msg += fmt.Sprintf(", including the database(s) %s", strings.Join(dbs, ", "))
			}
			g.UI().Printf("%s.", g.UI().Red(msg))
			g.UI().Hint("Buckets and database backups in Object Storage are kept (still billed); registering %s again doesn't get them back without an admin.", name)
			g.UI().Println()
			if cf.dryRun {
				return nil
			}
			if !cf.yes {
				ok, err := g.typeToConfirm(fmt.Sprintf("Type the app's name (%s) to delete it:", name), name)
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			if err := k.DeleteRegistryEntry(ctx, name); err != nil {
				return err
			}
			g.UI().Success("Removed %s", name)
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
		g.UI().Warn("can't check the cluster for an existing namespace %s: %v", name, err)
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
		g.UI().Warn("can't check %s: %v", e.Repo, err)
		return
	case !ok:
		g.UI().Warn("%s doesn't exist, or your GitHub login can't see it", e.Repo)
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
		g.UI().Warn("%s has no %s on %s yet; Flux reports the app as failing until it does", e.Repo, p, b)
	}
}

func (g *globals) waitDeployed(ctx context.Context, name string, timeout time.Duration) error {
	k, err := g.kube()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	step := g.UI().Step("Waiting for Flux to deploy " + name)
	for {
		st, err := k.AppStatus(ctx, name)
		if err == nil {
			msg := "not registered on the cluster yet"
			if st.Applied {
				msg = firstLine(st.Deploy.Message)
				if msg == "" {
					msg = "waiting for the first reconcile"
				}
			}
			step.Status("%s", msg)
			if st.Applied && st.Deploy.Ready() {
				step.Done("Deployed %s", name)
				g.UI().Hint("`mcloud status %s` shows its services.", name)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			step.Fail("Not deployed after %s", timeout)
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
	step := g.UI().Step("Waiting for Flux to create namespace " + name)
	for {
		if ok, err := k.NamespaceExists(ctx, name); err == nil && ok {
			step.Done("Namespace %s is ready", name)
			return nil
		}
		select {
		case <-ctx.Done():
			step.Fail("")
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

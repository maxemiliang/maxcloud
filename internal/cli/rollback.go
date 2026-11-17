package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/migrate"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// skipCI keeps GitHub Actions from running the deploy workflow for a
// rollback commit: the workflow builds the branch's code and would pin the
// newest image again.
const skipCI = "[skip ci]"

// deployJSON-like record of one deploy, for history and rollback.
type historyEntry struct {
	// ID names it for mcloud rollback --to: the deploy commit's SHA (GitOps)
	// or the deploy's number (direct).
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	By      string    `json:"by,omitempty"`
	Image   string    `json:"image,omitempty"`
	Message string    `json:"message,omitempty"`
	// Direct deploys: the working tree's commit, and whether it had
	// uncommitted changes.
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Functions applied (direct deploys).
	Functions []string `json:"functions,omitempty"`
	// RollbackOf is the deploy a rollback applied again.
	RollbackOf string `json:"rollbackOf,omitempty"`
	// Current: what's deployed now.
	Current bool `json:"current"`

	migrations []string // direct: what the image applies (nil: unknown)
}

type historyJSON struct {
	App     string         `json:"app"`
	Mode    string         `json:"mode"` // gitops or direct
	Repo    string         `json:"repo,omitempty"`
	Branch  string         `json:"branch,omitempty"`
	Path    string         `json:"path,omitempty"`
	Deploys []historyEntry `json:"deploys"`
}

// appHistory lists an app's deploys, newest first: a GitOps app's are the
// commits that changed its manifests directory; a direct app's are those
// mcloud recorded in its namespace.
func (g *globals) appHistory(ctx context.Context, k *kube.Client, name string, limit int) (*historyJSON, *kube.Status, error) {
	st, err := k.AppStatus(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if st.Entry == nil {
		return nil, nil, fmt.Errorf("%s isn't registered (mcloud apps list)", name)
	}
	h := &historyJSON{App: name, Deploys: []historyEntry{}}
	if st.Entry.Direct() {
		h.Mode = "direct"
		ds, err := k.DirectDeploys(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		for i, d := range ds {
			if i >= limit {
				break
			}
			e := historyEntry{
				ID: strconv.Itoa(d.Number), Time: d.Time, By: d.By, Image: d.Image, Commit: d.Commit, Dirty: d.Dirty,
				Functions: d.Functions, Current: i == 0, migrations: d.Migrations,
			}
			if d.RollbackOf > 0 {
				e.RollbackOf = strconv.Itoa(d.RollbackOf)
			}
			h.Deploys = append(h.Deploys, e)
		}
		return h, st, nil
	}
	if st.Entry.Repo == "" {
		return nil, nil, fmt.Errorf("%s is registered in the platform's git; its deploys are that repo's history", name)
	}
	h.Mode, h.Repo, h.Branch, h.Path = "gitops", st.Entry.Repo, st.Entry.Branch, st.Entry.Path
	if h.Branch == "" {
		h.Branch = registry.DefaultBranch
	}
	if h.Path == "" {
		h.Path = registry.DefaultPath
	}
	gh, err := g.github()
	if err != nil {
		return nil, nil, err
	}
	commits, err := gh.Commits(ctx, github.Repo(h.Repo), h.Branch, h.Path, limit)
	if err != nil {
		return nil, nil, err
	}
	// What Flux applied last; the deploy is the newest change to the
	// manifests at or before it.
	current := ""
	if sha := appliedSHA(st.Revision); sha != "" {
		if cs, err := gh.Commits(ctx, github.Repo(h.Repo), sha, h.Path, 1); err == nil && len(cs) > 0 {
			current = cs[0].SHA
		}
	}
	if current == "" && len(commits) > 0 {
		current = commits[0].SHA
	}
	for _, c := range commits {
		e := historyEntry{ID: c.SHA, Time: c.Date, By: c.Author, Message: c.Subject(), Image: imageOf(c.Message), Current: c.SHA == current}
		if s, ok := strings.CutPrefix(e.Message, "rollback: to "); ok {
			if f := strings.Fields(s); len(f) > 0 {
				e.RollbackOf = f[0]
			}
		}
		h.Deploys = append(h.Deploys, e)
	}
	return h, st, nil
}

// appliedSHA is the commit in Flux's "main@sha1:<sha>".
func appliedSHA(rev string) string {
	_, sha, ok := strings.Cut(rev, "@sha1:")
	if !ok {
		return ""
	}
	return sha
}

// imageOf finds the "Image <ref>" line the deploy workflow writes into its
// commits (and rollback commits repeat).
func imageOf(message string) string {
	for _, l := range strings.Split(message, "\n") {
		if s, ok := strings.CutPrefix(strings.TrimSpace(l), "Image "); ok && strings.Contains(s, "@sha256:") {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func historyCmd(g *globals) *cobra.Command {
	var limit int
	var asJSON bool
	c := &cobra.Command{
		Use:   "history [APP]",
		Short: "List an app's past deploys, to roll back to one",
		Long: `List an app's deploys, newest first, and which one is deployed now.

A GitOps app's deploys are the commits that changed its manifests
(deploy/k8s): the deploy workflow's, each pinning an image, and rollbacks.
They're read from GitHub. A direct app's are recorded in its namespace by
mcloud deploy (the last 10), with what each applied.

mcloud rollback --to <ID> goes back to one.`,
		Example: `  mcloud history
  mcloud history shop --limit 30
  mcloud history --json | jq -r '.deploys[1].id'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			h, _, err := g.appHistory(cmd.Context(), k, name, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return g.printJSON(h)
			}
			printHistory(g, h)
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 15, "how many deploys to list (GitOps: up to 100)")
	jsonFlag(c, &asJSON)
	return c
}

func printHistory(g *globals, h *historyJSON) {
	u := g.UI()
	if len(h.Deploys) == 0 {
		if h.Mode == "direct" {
			u.Note("no deploys recorded for %s yet: mcloud deploy records each one", h.App)
		} else {
			u.Note("no commits change %s in %s@%s", h.Path, h.Repo, h.Branch)
		}
		return
	}
	var rows [][]string
	for _, d := range h.Deploys {
		id := d.ID
		if h.Mode == "gitops" {
			id = shortSHA(id)
		}
		mark := " "
		if d.Current {
			mark = u.Green("●")
		}
		what := d.Message
		if h.Mode == "direct" {
			what = strings.Join(d.Functions, ", ")
			if d.Commit != "" {
				what = "from " + shortSHA(d.Commit)
				if d.Dirty {
					what += "+changes"
				}
				what += ": " + strings.Join(d.Functions, ", ")
			}
			if d.RollbackOf != "" {
				what = "rollback to " + d.RollbackOf + "; " + what
			}
		}
		rows = append(rows, []string{mark + " " + u.Accent(id), u.Dim(d.Time.Local().Format("2006-01-02 15:04")), d.By, imageShort(d.Image), truncate(what, 60)})
	}
	u.Table([]string{"  Deploy", "When", "By", "Image", "What"}, rows)
	if h.Mode == "gitops" {
		u.Hint("● is what Flux applied last. From %s@%s:%s", h.Repo, h.Branch, h.Path)
	} else {
		u.Hint("● is deployed now.")
	}
}

// imageShort is an image digest's first 12 characters.
func imageShort(image string) string {
	if _, d, ok := strings.Cut(image, "@sha256:"); ok {
		if len(d) > 12 {
			d = d[:12]
		}
		return d
	}
	if image == "" {
		return "-"
	}
	return truncate(image, 20)
}

func rollbackCmd(g *globals) *cobra.Command {
	var to string
	var yes, dryRun bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "rollback [APP]",
		Short: "Deploy an earlier deploy again",
		Long: `Go back to an earlier deploy: the one before what's deployed now, or the
one --to names (an ID from mcloud history). It shows what changes and
asks first.

A GitOps app rolls back with a commit: the manifests directory
(deploy/k8s) as it was at that deploy, committed on top of the branch
through GitHub with your GitHub login, marked [skip ci] so the deploy
workflow doesn't build the branch again over it. Flux applies it like
any deploy (it would undo a change made by hand). The code on the branch
doesn't change: the next push builds it and deploys forward again, so
fix or revert the bad commit before you push.

A direct app rolls back by applying what that deploy applied again, the
way mcloud deploy does: its HTTP functions get a new Knative revision of
the old image, its scheduled and queue functions their old CronJob,
ScaledJob or Deployment.

What a rollback doesn't undo:
  - the database: migrations only go forward. When the database has
    migrations the target deploy doesn't know, mcloud says which, and the
    old code then runs on the newer schema. Going back to the data as it
    was is mcloud db restore, which loses what was written since.
  - variables from mcloud env set and secrets, and the app's buckets,
    database and domains (maxcloud.yaml's env: is in the manifests, and
    does roll back).`,
		Example: `  mcloud rollback                  # to the deploy before the current one
  mcloud history && mcloud rollback shop --to 3f2a9c1
  mcloud rollback --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			h, st, err := g.appHistory(ctx, k, name, 100)
			if err != nil {
				return err
			}
			cur, target, err := rollbackTarget(h, to)
			if err != nil {
				return err
			}
			u := g.UI()
			label := func(d *historyEntry) string {
				id := d.ID
				if h.Mode == "gitops" {
					id = shortSHA(id)
				}
				s := u.Bold(id) + " " + u.Dim(d.Time.Local().Format("2006-01-02 15:04"))
				if d.Message != "" {
					s += " " + truncate(d.Message, 50)
				}
				if d.Image != "" {
					s += u.Dim(" (" + imageShort(d.Image) + ")")
				}
				return s
			}
			kv := [][2]string{{"App", u.Bold(name)}}
			if cur != nil {
				kv = append(kv, [2]string{"Now", label(cur)})
			}
			kv = append(kv, [2]string{"Back to", label(target)})
			if h.Mode == "gitops" {
				kv = append(kv, [2]string{"How", fmt.Sprintf("a commit %s on %s@%s restoring %s", skipCI, h.Repo, h.Branch, h.Path)})
			} else {
				kv = append(kv, [2]string{"How", "apply deploy " + target.ID + "'s objects again, as mcloud deploy would"})
			}
			u.KV(kv)
			g.warnMigrations(ctx, k, h, target)
			u.Println()
			if dryRun {
				return nil
			}
			if !yes {
				ok, err := g.confirm("Roll back?")
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			started := time.Now()
			if h.Mode == "direct" {
				return g.rollbackDirect(ctx, k, name, target)
			}
			return g.rollbackGitOps(ctx, k, st, h, target, started)
		},
	}
	f := c.Flags()
	f.StringVar(&to, "to", "", "the deploy to go back to (an ID from mcloud history; default: the one before the current one)")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask first")
	f.BoolVar(&dryRun, "dry-run", false, "show what would happen, change nothing")
	f.DurationVar(&timeout, "timeout", 10*time.Minute, "give up following the rollout after this long")
	return c
}

// rollbackTarget picks the current deploy and the one to go back to: to
// (an ID or a prefix of a SHA), else the one before the current one.
func rollbackTarget(h *historyJSON, to string) (cur, target *historyEntry, err error) {
	ci := slices.IndexFunc(h.Deploys, func(d historyEntry) bool { return d.Current })
	if ci >= 0 {
		cur = &h.Deploys[ci]
	}
	if to == "" {
		if ci < 0 || ci+1 >= len(h.Deploys) {
			return cur, nil, fmt.Errorf("%s has no deploy before the current one to go back to (mcloud history)", h.App)
		}
		return cur, &h.Deploys[ci+1], nil
	}
	var found []*historyEntry
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.ID == to || h.Mode == "gitops" && len(to) >= 4 && strings.HasPrefix(d.ID, to) {
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 0:
		return cur, nil, fmt.Errorf("no deploy %s of %s (mcloud history lists them)", to, h.App)
	case len(found) > 1:
		return cur, nil, fmt.Errorf("%s names more than one deploy; give more of it", to)
	case found[0].Current:
		return cur, nil, fmt.Errorf("%s is what's deployed now", to)
	}
	return cur, found[0], nil
}

// warnMigrations says which migrations the database has that target
// doesn't: a rollback leaves the schema as it is.
func (g *globals) warnMigrations(ctx context.Context, k *kube.Client, h *historyJSON, target *historyEntry) {
	u := g.UI()
	if ok, err := k.Exists(ctx, kube.GVRCNPGCluster, h.App, functions.DatabaseName); err != nil || !ok {
		return
	}
	var known []string
	switch h.Mode {
	case "direct":
		if target.migrations == nil {
			u.Note("deploy %s didn't record its migrations; mcloud can't tell whether the database is ahead of it", target.ID)
			return
		}
		known = target.migrations
	default:
		names, ok, err := g.gitMigrations(ctx, h, target.ID)
		if err != nil {
			u.Note("couldn't read the migrations at %s: %v", shortSHA(target.ID), err)
			return
		}
		if !ok {
			return
		}
		known = names
	}
	applied, ok, err := k.AppliedMigrations(ctx, h.App)
	if err != nil {
		u.Note("couldn't read the database's migrations: %v", err)
		return
	}
	if !ok {
		return
	}
	var ahead []string
	for _, n := range applied {
		if !slices.Contains(known, n) {
			ahead = append(ahead, n)
		}
	}
	if len(ahead) == 0 {
		return
	}
	u.Println()
	u.Warn("the database has %d migration(s) deploy %s doesn't know: %s", len(ahead), shortID(h, target.ID), strings.Join(ahead, ", "))
	u.Hint("A rollback doesn't undo them: migrations only go forward, so the old code runs on the newer schema.")
	u.Hint("If it can't, roll forward with a fix instead; mcloud db restore --at <time> brings back the data as it was, losing what was written since.")
}

func shortID(h *historyJSON, id string) string {
	if h.Mode == "gitops" {
		return shortSHA(id)
	}
	return id
}

// gitMigrations lists the SQL migrations in the app's repo at a commit:
// migrations/ in source: (from this checkout's maxcloud.yaml, if it's the
// app's), else at the root. ok is false for none, or drizzle-kit's.
func (g *globals) gitMigrations(ctx context.Context, h *historyJSON, sha string) ([]string, bool, error) {
	src := ""
	if p, err := loadProject(); err == nil && p.App == h.App && p.Source != "" && p.Source != "." {
		src = strings.Trim(p.Source, "/")
	}
	dir := path.Join(src, migrate.Dir)
	gh, err := g.github()
	if err != nil {
		return nil, false, err
	}
	files, err := gh.Dir(ctx, github.Repo(h.Repo), sha, dir)
	if errors.Is(err, github.ErrNotFound) {
		return []string{}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if ok, _ := gh.Exists(ctx, github.Repo(h.Repo), sha, path.Join(dir, "meta", "_journal.json")); ok {
		return nil, false, nil // drizzle-kit's, applied by the app's own migrator
	}
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, ".sql") {
			names = append(names, f)
		}
	}
	return names, true, nil
}

// rollbackGitOps commits the manifests as they were at target, and follows
// Flux applying them.
func (g *globals) rollbackGitOps(ctx context.Context, k *kube.Client, st *kube.Status, h *historyJSON, target *historyEntry, started time.Time) error {
	gh, err := g.github()
	if err != nil {
		return err
	}
	u := g.UI()
	msg := fmt.Sprintf("rollback: to %s %s\n\nRestores %s as it was at %s (%s), with mcloud rollback.\nThe deploy workflow doesn't run for this commit; the next push deploys\nthe branch's code again.",
		shortSHA(target.ID), skipCI, h.Path, shortSHA(target.ID), target.Message)
	if target.Image != "" {
		msg += "\n\nImage " + target.Image
	}
	step := u.Step(fmt.Sprintf("Committing %s as of %s to %s", h.Path, shortSHA(target.ID), h.Repo))
	sha, err := gh.RestoreDir(ctx, github.Repo(h.Repo), h.Branch, h.Path, target.ID, msg)
	if err != nil {
		step.Fail("")
		return err
	}
	if sha == "" {
		step.Skip("%s is already as it was at %s", h.Path, shortSHA(target.ID))
		return nil
	}
	step.Done("Committed %s", u.Accent(shortSHA(sha)))
	if err := g.followFlux(ctx, k, st.Name, sha); err != nil {
		return err
	}
	if err := g.followServices(ctx, k, st.Name, started); err != nil {
		return err
	}
	u.Hint("The next push to %s builds its code and deploys forward again.", h.Branch)
	return nil
}

// rollbackDirect applies a recorded deploy's objects again.
func (g *globals) rollbackDirect(ctx context.Context, k *kube.Client, name string, target *historyEntry) error {
	n, _ := strconv.Atoi(target.ID)
	objs, err := k.DeployObjects(ctx, name, n)
	if err != nil {
		return err
	}
	u := g.UI()
	started := time.Now()
	step := u.Step(fmt.Sprintf("Applying deploy %d", n))
	if err := k.Apply(ctx, name, objs); err != nil {
		step.Fail("")
		return err
	}
	pruned, err := k.Prune(ctx, name, objs)
	if err != nil {
		step.Fail("")
		return err
	}
	step.Done("Applied deploy %d", n)
	for _, p := range pruned {
		u.Hint("removed   %s (not in deploy %d)", p, n)
	}
	g.recordDirectDeploy(ctx, k, name, kube.DirectDeploy{
		Image: target.Image, Commit: target.Commit, Dirty: target.Dirty, Functions: target.Functions,
		Migrations: target.migrations, RollbackOf: n,
	}, objs)
	return g.followServices(ctx, k, name, started)
}

// recordDirectDeploy keeps a direct deploy in the app's history (for
// mcloud history and rollback). A failure only warns: the deploy is done.
func (g *globals) recordDirectDeploy(ctx context.Context, k *kube.Client, app string, d kube.DirectDeploy, objs []map[string]any) {
	d.Time = time.Now().UTC()
	if d.By == "" {
		if who, err := k.Whoami(ctx); err == nil {
			d.By = strings.TrimPrefix(who, "authelia:")
		}
	}
	if _, err := k.RecordDeploy(ctx, app, d, objs); err != nil {
		g.UI().Warn("couldn't record this deploy for mcloud history/rollback: %v", err)
	}
}

// directDeployRecord describes a direct deploy of the working tree.
func directDeployRecord(image string, d *functions.Description) kube.DirectDeploy {
	rec := kube.DirectDeploy{Image: image, Migrations: d.Migrations}
	for _, f := range d.Functions {
		rec.Functions = append(rec.Functions, f.Kind+"/"+f.Name)
	}
	if sha, err := git("rev-parse", "HEAD"); err == nil {
		rec.Commit = sha
		if dirty, _ := git("status", "--porcelain"); dirty != "" {
			rec.Dirty = true
		}
	}
	return rec
}

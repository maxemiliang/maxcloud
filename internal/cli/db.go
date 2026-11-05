package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

func dbCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "db",
		Short: "An app's database: restore it, recover it, branch it",
		Long: `For apps whose database the maxcloud controller runs (mcloud apps set <app>
--db). Its backups are generations: db-1, db-2, ... in the bucket
mc-<app>--db; every restore starts a new one and leaves the older ones as
they are.`,
	}
	c.AddCommand(dbRestoreCmd(g), dbRecoverCmd(g), dbBranchCmd(g))
	return c
}

func dbRestoreCmd(g *globals) *cobra.Command {
	var at string
	var yes, wait bool
	c := &cobra.Command{
		Use:   "restore [APP] --at TIME",
		Short: "Restore the database, in place, to how it was at a point in time",
		Long: `Takes the database down and brings it back as it was at TIME, from its
backups. Everything written after TIME is gone from the database; it stays
in the older generation's backups for as long as they're kept.

TIME is RFC 3339 (2026-11-05T10:00:00Z), a local date and time
(2026-11-05 10:00), or how long ago (15m, 2h).

To try a point in time without touching the database, branch it instead:
mcloud db branch create <name> --at TIME.`,
		Example: `  mcloud db restore shop --at 15m
  mcloud db restore shop --at "2026-11-05 10:00"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			t, err := parseAt(at, time.Now())
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			db, err := k.GetDatabase(cmd.Context(), name)
			if err != nil {
				return err
			}
			if db == nil {
				return fmt.Errorf("app %s has no database run by maxcloud (mcloud apps set %s --db)", name, name)
			}
			fmt.Fprintf(g.out, "This takes %s's database down and restores it to %s (%s).\nEverything written since then will be gone from it.\n",
				name, t.Local().Format("2006-01-02 15:04:05 MST"), t.UTC().Format(time.RFC3339))
			if !yes {
				ok, err := g.typeToConfirm(fmt.Sprintf("Type the app's name (%s) to restore: ", name), name)
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			if err := k.AnnotateDatabase(cmd.Context(), name, kube.RestoreToAnnotation, t.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			fmt.Fprintln(g.out, "Restore requested.")
			if !wait {
				fmt.Fprintf(g.out, "Follow it: kubectl get maxcloud -n %s\n", name)
				return nil
			}
			return g.waitDatabase(cmd.Context(), k, name, db.Generation)
		},
	}
	c.Flags().StringVar(&at, "at", "", "the point in time (required)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	c.Flags().BoolVar(&wait, "wait", true, "wait until the database is back")
	_ = c.MarkFlagRequired("at")
	return c
}

func dbRecoverCmd(g *globals) *cobra.Command {
	var from int64
	c := &cobra.Command{
		Use:   "recover [APP] --from GENERATION",
		Short: "Unblock a Blocked database by picking the backup generation to restore",
		Long: `The controller stops (Blocked) instead of guessing when it can't account
for what's in the backup bucket. Look at the generations (the folders db-N
in mc-<app>--db), pick one, and it restores that one into a new
generation.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			if from < 1 {
				return errors.New("--from: a generation number, 1 or more")
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			db, err := k.GetDatabase(cmd.Context(), name)
			if err != nil {
				return err
			}
			if db == nil {
				return fmt.Errorf("app %s has no database run by maxcloud", name)
			}
			if db.Phase != "Blocked" {
				return fmt.Errorf("%s's database isn't Blocked (it's %s): nothing to recover. To go back in time, use mcloud db restore", name, db.Phase)
			}
			if err := k.AnnotateDatabase(cmd.Context(), name, kube.RecoverFromAnnotation, strconv.FormatInt(from, 10)); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Asked to restore generation %d.\n", from)
			return g.waitDatabase(cmd.Context(), k, name, db.Generation)
		},
	}
	c.Flags().Int64Var(&from, "from", 0, "the backup generation to restore (required)")
	_ = c.MarkFlagRequired("from")
	return c
}

// waitDatabase follows the database until it's Ready on a newer generation
// than before, Blocked, or a restore was refused.
func (g *globals) waitDatabase(ctx context.Context, k *kube.Client, app string, before int64) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	last := ""
	for {
		db, err := k.GetDatabase(ctx, app)
		if err != nil {
			return err
		}
		if db != nil {
			line := fmt.Sprintf("%s, generation %d: %s", db.Phase, db.Generation, db.Ready)
			if line != last {
				fmt.Fprintln(g.out, "  "+line)
				last = line
			}
			switch {
			case db.Phase == "Ready" && db.Generation > before:
				fmt.Fprintf(g.out, "Done: %s\n", db.Restore)
				return nil
			case db.Phase == "Blocked":
				return fmt.Errorf("blocked: %s", db.Blocked)
			case strings.Contains(db.Restore, "was left as it was"):
				return errors.New(db.Restore)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("still not back after 15 minutes: kubectl get maxcloud,cluster,backup -n %s", app)
		case <-time.After(5 * time.Second):
		}
	}
}

func dbBranchCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "branch",
		Short: "Throwaway copies of the database, now or at a point in time",
		Long: `A branch is the app's database restored from its backups into a
Postgres of its own (Cluster db-<name>; its URL is in the Secret
db-<name>-app, key uri). It doesn't back itself up and changes nothing in
the database: for trying a migration, a preview deploy, or looking at the
data as it was. It counts against the app's quota until you delete it.`,
	}
	c.AddCommand(dbBranchCreateCmd(g), dbBranchListCmd(g), dbBranchDeleteCmd(g))
	return c
}

var branchNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9]|-[a-z0-9])*$`)

func dbBranchCreateCmd(g *globals) *cobra.Command {
	var at string
	var wait bool
	c := &cobra.Command{
		Use:   "create [APP] NAME",
		Short: "Branch the database",
		Example: `  mcloud db branch create shop try-migration
  mcloud db branch create shop yesterday --at 24h`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, branch, err := appAndName(args)
			if err != nil {
				return err
			}
			if !branchNameRe.MatchString(branch) || len(branch) > 30 {
				return fmt.Errorf("branch %q: lowercase letters, digits and single dashes, at most 30 characters", branch)
			}
			var t *time.Time
			if at != "" {
				v, err := parseAt(at, time.Now())
				if err != nil {
					return err
				}
				t = &v
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			if err := k.CreateBranch(cmd.Context(), name, branch, t); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Branch %s of %s's database requested (Cluster db-%s).\n", branch, name, branch)
			if !wait {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			for {
				b, err := k.GetBranch(ctx, name, branch)
				if err != nil {
					return err
				}
				switch b.Phase {
				case "Ready":
					fmt.Fprintf(g.out, "Ready. Its URL: kubectl -n %s get secret db-%s-app -o jsonpath='{.data.uri}' | base64 -d\n", name, branch)
					return nil
				case "Failed":
					return errors.New(b.Message)
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("not ready after 15 minutes: kubectl get maxcloud,cluster -n %s", name)
				case <-time.After(5 * time.Second):
				}
			}
		},
	}
	c.Flags().StringVar(&at, "at", "", "the point in time (default: the latest)")
	c.Flags().BoolVar(&wait, "wait", true, "wait until the branch is ready")
	return c
}

func dbBranchListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list [APP]",
		Short: "List the database's branches",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			bs, err := k.Branches(cmd.Context(), name)
			if err != nil {
				return err
			}
			if len(bs) == 0 {
				fmt.Fprintln(g.out, "(no branches)")
				return nil
			}
			w := tabwriter.NewWriter(g.out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPHASE\tCLUSTER\tAT\tFROM GEN\tAGE")
			for _, b := range bs {
				at := b.At
				if at == "" {
					at = "latest"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", b.Name, b.Phase, b.Cluster, at, b.FromGeneration, time.Since(b.Created).Round(time.Minute))
			}
			return w.Flush()
		},
	}
}

func dbBranchDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete [APP] NAME",
		Short: "Delete a branch and its Postgres",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, branch, err := appAndName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			if err := k.DeleteBranch(cmd.Context(), name, branch); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Deleted branch %s (and Cluster db-%s).\n", branch, branch)
			return nil
		},
	}
}

// appAndName reads [APP] NAME: with one argument, the app comes from
// maxcloud.yaml.
func appAndName(args []string) (app, name string, err error) {
	if len(args) == 2 {
		return args[0], args[1], nil
	}
	app, err = appName(nil)
	return app, args[0], err
}

// parseAt reads a point in time: RFC 3339, a local "2006-01-02 15:04"
// (seconds optional), or a duration ago ("15m", "2h30m").
func parseAt(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--at %q: how long ago, e.g. 15m", s)
		}
		return now.Add(-d).Truncate(time.Second), nil
	}
	var t time.Time
	var err error
	if t, err = time.Parse(time.RFC3339, s); err != nil {
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
			if t, err = time.ParseInLocation(layout, s, time.Local); err == nil {
				break
			}
		}
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("--at %q: use 2026-11-05T10:00:00Z, \"2026-11-05 10:00\" (local time) or how long ago (15m)", s)
	}
	if !t.Before(now) {
		return time.Time{}, fmt.Errorf("--at %q is in the future", s)
	}
	return t, nil
}

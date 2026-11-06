package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/migrate"
)

func dbMigrateCmd(g *globals) *cobra.Command {
	var branch, dir, url string
	var dryRun, yes bool
	c := &cobra.Command{
		Use:   "migrate [APP]",
		Short: "Apply the app's SQL migrations (migrations/*.sql) to its database",
		Long: `Apply the files in migrations/ (next to maxcloud.yaml, or --dir) that the
database hasn't had yet, in name order, each in its own transaction. Name
them NUMBER_DESCRIPTION.sql with numbers of one width: 0001_init.sql,
0002_add_orders.sql. The database records each one it applied, with a
checksum, in the table maxcloud_migrations; a file edited after it was
applied stops the run. Only one run applies at a time.

It reaches the deployed app's database (or a branch's) through a
port-forward, as the app's own user, and asks before it changes the app's
database. --database-url applies them to any Postgres instead.

Apps on the SDKs don't need this to deploy: they apply their pending
migrations themselves when they start. mcloud dev applies them to the
database it runs the app with.`,
		Example: `  mcloud db migrate --dry-run
  mcloud db migrate
  mcloud db migrate shop --branch try-migration
  mcloud db migrate --database-url postgres://postgres:dev@127.0.0.1:5432/app`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, where, err := loadMigrations(dir)
			if err != nil {
				return err
			}
			u := g.UI()
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			target := ""
			ask := false
			if url == "" {
				name, err := appName(args)
				if err != nil {
					return err
				}
				k, err := g.kube()
				if err != nil {
					return err
				}
				if url, err = k.ProxyDatabase(ctx, name, dbCluster(branch), 0); err != nil {
					return err
				}
				target = name + "'s database"
				if branch != "" {
					target = name + "'s database branch " + branch
				}
				ask = branch == ""
			} else {
				target = describeURL(url)
			}
			conn, err := pgx.Connect(ctx, url)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", target, err)
			}
			defer conn.Close(context.WithoutCancel(ctx))

			st, err := migrate.Status(ctx, conn, files)
			if err != nil {
				return err
			}
			pending, changed := 0, 0
			var rows [][]string
			for _, e := range st {
				state := string(e.State)
				switch e.State {
				case migrate.Applied:
					state = u.Green(state)
				case migrate.Pending:
					state = u.Amber(state)
					pending++
				case migrate.Changed:
					state = u.Red(state + " since it was applied")
					changed++
				case migrate.Missing:
					state = u.Dim("applied, not in " + where)
				}
				at := ""
				if !e.AppliedAt.IsZero() {
					at = u.Dim(e.AppliedAt.Local().Format("2006-01-02 15:04"))
				}
				rows = append(rows, []string{e.Name, state, at})
			}
			u.KV([][2]string{{"database", target}, {"migrations", where}})
			if len(rows) > 0 {
				u.Table([]string{"Migration", "State", "Applied"}, rows)
			}
			if dryRun {
				u.Note("%d pending; --dry-run applies none", pending)
				return nil
			}
			if pending == 0 || changed > 0 {
				// Apply refuses a changed file before it applies anything.
				if _, err := migrate.Apply(ctx, conn, files, nil); err != nil {
					return err
				}
				u.Success("Up to date")
				return nil
			}
			if ask && !yes {
				ok, err := g.confirm(fmt.Sprintf("Apply %d migration(s) to %s?", pending, target))
				if err != nil || !ok {
					return orAborted(err)
				}
			}
			return g.applyMigrations(ctx, conn, files, target)
		},
	}
	c.Flags().StringVar(&branch, "branch", "", "a branch of the database (mcloud db branch list)")
	c.Flags().StringVar(&dir, "dir", "", "the migrations directory (default: migrations/ next to maxcloud.yaml)")
	c.Flags().StringVar(&url, "database-url", "", "apply to this Postgres instead of the app's database")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list the applied and pending migrations, apply none")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	return c
}

// loadMigrations reads dir (default: migrations/ next to maxcloud.yaml, or
// here) and returns the files and how to name the directory.
func loadMigrations(dir string) ([]migrate.File, string, error) {
	explicit := dir != ""
	if !explicit {
		dir = migrate.Dir
		if p, err := loadProject(); err == nil {
			dir = filepath.Join(p.Dir, migrate.Dir)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	where := dir
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			where = rel
		}
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		if explicit {
			return nil, "", fmt.Errorf("--dir %s: not a directory", dir)
		}
		return nil, "", fmt.Errorf("no %s directory: put the migrations in migrations/ next to maxcloud.yaml (0001_init.sql, ...), or pass --dir", where)
	}
	files, err := migrate.Load(os.DirFS(filepath.Dir(abs)), filepath.Base(abs))
	return files, where, err
}

// applyMigrations applies the pending migrations and says what it did.
func (g *globals) applyMigrations(ctx context.Context, conn *pgx.Conn, files []migrate.File, target string) error {
	step := g.UI().Step("Migrating " + target)
	names, err := migrate.Apply(ctx, conn, files, func(m string) { step.Status("%s", m) })
	if err != nil {
		step.Fail("Migration failed (%d applied before it)", len(names))
		return err
	}
	if len(names) == 0 {
		step.Done("Up to date")
		return nil
	}
	step.Done("Applied %d migration(s): %s", len(names), strings.Join(names, ", "))
	return nil
}

// devMigrate applies the app's pending migrations to the database mcloud
// dev hands it, if the app has any.
func (g *globals) devMigrate(ctx context.Context, p *project, url string) error {
	files, err := migrate.Load(os.DirFS(p.Dir), migrate.Dir)
	if err != nil || len(files) == 0 {
		return err
	}
	if url == "" {
		g.UI().Warn("%s has %d migration(s), but the app has no database here (--db local)", migrate.Dir, len(files))
		return nil
	}
	// A new local Postgres answers pg_isready on its socket while it still
	// initializes, before it listens on TCP: give it a few seconds.
	var conn *pgx.Conn
	for i := 0; ; i++ {
		if conn, err = pgx.Connect(ctx, url); err == nil {
			break
		}
		if i == 20 {
			return fmt.Errorf("connecting to the database to migrate it: %w", err)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if err := g.applyMigrations(ctx, conn, files, "the database"); err != nil {
		if errors.Is(err, migrate.ErrChanged) {
			g.UI().Hint("a local Postgres starts over with: docker rm -f mcloud-dev-%s; docker volume rm mcloud-dev-%s", p.App, p.App)
		}
		return err
	}
	return nil
}

// describeURL is a database URL without its password: host:port/database.
func describeURL(url string) string {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return "the database"
	}
	return fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}

// imageMigrationsWarning says what's wrong when the repo has migrations
// that the built image won't apply when it starts.
func imageMigrationsWarning(d *functions.Description) string {
	p, err := loadProject()
	if err != nil {
		return ""
	}
	files, err := migrate.Load(os.DirFS(p.Dir), migrate.Dir)
	if err != nil {
		return err.Error()
	}
	if len(files) == 0 {
		return ""
	}
	if d.Migrations == nil {
		return fmt.Sprintf("%s/ has %d migration(s), but this %s (%s) doesn't apply migrations when the app starts: update the SDK, or apply them with mcloud db migrate", migrate.Dir, len(files), d.SDK, d.Version)
	}
	has := map[string]bool{}
	for _, n := range d.Migrations {
		has[n] = true
	}
	var missing []string
	for _, f := range files {
		if !has[f.Name] {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("the image doesn't have %s/%s, so it won't apply it. TypeScript: copy migrations/ into the image next to package.json (buildpacks do). Go: //go:embed migrations and functions.Migrations(...)", migrate.Dir, strings.Join(missing, ", "+migrate.Dir+"/"))
}

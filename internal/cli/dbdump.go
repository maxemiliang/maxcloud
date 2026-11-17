package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The Postgres database and role every maxcloud app database has
// (CloudNativePG's defaults): the database the app's tables are in, and
// the user its functions connect as.
const (
	appDB     = "app"
	appDBRole = "app"
)

// customDumpMagic starts a pg_dump custom-format archive (-Fc).
const customDumpMagic = "PGDMP"

func dbDumpCmd(g *globals) *cobra.Command {
	var branch, out, format string
	var schemaOnly, dataOnly bool
	c := &cobra.Command{
		Use:   "dump [APP]",
		Short: "Write the database (or a branch) to a file with pg_dump",
		Long: `Dump the app's database with pg_dump, run in the database's primary, and
write it here: for a copy on your machine, moving data to another app
(mcloud db import), or a look at the schema.

The dump names no owner and no grants (--no-owner, --no-privileges), so it
loads into any Postgres, as whoever loads it. --format custom writes
pg_dump's compressed archive instead of SQL; pg_restore and mcloud db
import read it.

It isn't the backup: the database is backed up continuously by itself
(mcloud db restore, mcloud db branch). A dump is one moment, as consistent
as one transaction.`,
		Example: `  mcloud db dump                          # shop-20261117-1030.sql
  mcloud db dump --schema-only -o schema.sql
  mcloud db dump -o - | grep 'CREATE TABLE'
  mcloud db dump shop --branch try --format custom -o try.dump`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			pgDump, err := dumpCommand(format, schemaOnly, dataOnly)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			pod, err := k.PrimaryPod(cmd.Context(), name, dbCluster(branch))
			if err != nil {
				return err
			}
			if out == "-" {
				return k.Exec(cmd.Context(), name, pod, "postgres", pgDump, nil, g.out, g.errOut)
			}
			if out == "" {
				ext := map[bool]string{false: ".sql", true: ".dump"}[format == "custom"]
				out = name + "-" + time.Now().Format("20060102-1504") + ext
				if branch != "" {
					out = name + "-" + branch + "-" + time.Now().Format("20060102-1504") + ext
				}
			}
			// 0600: a dump holds everything the database does.
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s exists already: remove it, or name another file with -o", out)
			}
			if err != nil {
				return err
			}
			step := g.UI().Step("Dumping " + name + "'s database" + map[bool]string{true: " branch " + branch}[branch != ""])
			err = k.Exec(cmd.Context(), name, pod, "postgres", pgDump, nil, f, g.errOut)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(out) // half a dump is worse than none
				step.Fail("pg_dump failed")
				return fmt.Errorf("pg_dump in %s/%s: %w", name, pod, err)
			}
			size := int64(0)
			if st, err := os.Stat(out); err == nil {
				size = st.Size()
			}
			step.Done("Wrote " + out + " (" + sizeString(size) + ")")
			return nil
		},
	}
	c.Flags().StringVar(&branch, "branch", "", "a branch of the database (mcloud db branch list)")
	c.Flags().StringVarP(&out, "output", "o", "", "the file to write (default <app>-<date>-<time>.sql; - for stdout)")
	c.Flags().StringVar(&format, "format", "plain", "plain (SQL) or custom (pg_dump's compressed archive)")
	c.Flags().BoolVar(&schemaOnly, "schema-only", false, "the tables and other definitions, no rows")
	c.Flags().BoolVar(&dataOnly, "data-only", false, "the rows, no definitions")
	return c
}

// dumpCommand is the pg_dump to run in the database's primary: as the
// postgres superuser over the local socket, like mcloud db psql.
func dumpCommand(format string, schemaOnly, dataOnly bool) ([]string, error) {
	cmd := []string{"pg_dump", "-d", appDB, "--no-owner", "--no-privileges"}
	switch format {
	case "plain", "":
	case "custom":
		cmd = append(cmd, "--format=custom")
	default:
		return nil, fmt.Errorf("--format %s: it's plain or custom", format)
	}
	switch {
	case schemaOnly && dataOnly:
		return nil, errors.New("--schema-only and --data-only together would dump nothing")
	case schemaOnly:
		cmd = append(cmd, "--schema-only")
	case dataOnly:
		cmd = append(cmd, "--data-only")
	}
	return cmd, nil
}

func dbImportCmd(g *globals) *cobra.Command {
	var branch string
	var yes bool
	c := &cobra.Command{
		Use:   "import [APP] FILE",
		Short: "Load a dump into the database (or a branch)",
		Long: `Load a dump into the app's database: SQL (what mcloud db dump and plain
pg_dump write) with psql, or a custom-format archive (pg_dump -Fc) with
pg_restore. mcloud tells them apart by the file's first bytes. FILE - reads
stdin.

It loads as the app's own user, so the app owns what's created, and in one
transaction: a dump that fails halfway leaves the database as it was. It
adds to what's there. Loading into tables that already exist fails (and
changes nothing); for a clean slate, import into a new branch, or drop
what's in the way first.

It asks before it writes to the deployed database (-y doesn't). Try it on
a branch first: mcloud db branch create try, then --branch try.`,
		Example: `  mcloud db import shop-20261117-1030.sql
  mcloud db import shop old.dump --branch try
  pg_dump --no-owner --no-privileges "$OLD_DATABASE_URL" | mcloud db import -`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := args[len(args)-1]
			name, err := appName(args[:len(args)-1])
			if err != nil {
				return err
			}
			var src io.Reader = g.in
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			}
			// Peek at the format; the reader then still has all of it.
			in := bufio.NewReader(src)
			head, err := in.Peek(len(customDumpMagic))
			if err != nil && len(head) == 0 {
				return fmt.Errorf("%s is empty", file)
			}
			custom := isCustomDump(head)

			k, err := g.kube()
			if err != nil {
				return err
			}
			pod, err := k.PrimaryPod(cmd.Context(), name, dbCluster(branch))
			if err != nil {
				return err
			}
			target := name + "'s deployed database"
			if branch != "" {
				target = name + "'s database branch " + branch
			}
			if branch == "" && !yes {
				ok, err := g.confirm("Load " + file + " into " + target + "?")
				if err != nil {
					return fmt.Errorf("%w (-y loads without asking)", err)
				}
				if !ok {
					return errors.New("nothing loaded")
				}
			}
			step := g.UI().Step("Loading " + file + " into " + target)
			if err := k.Exec(cmd.Context(), name, pod, "postgres", importCommand(custom), in, io.Discard, g.errOut); err != nil {
				step.Fail("Nothing loaded: the database is as it was")
				return fmt.Errorf("%s in %s/%s: %w", map[bool]string{false: "psql", true: "pg_restore"}[custom], name, pod, err)
			}
			step.Done("Loaded " + file + " into " + target)
			return nil
		},
	}
	c.Flags().StringVar(&branch, "branch", "", "load into a branch of the database (mcloud db branch list)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask before loading into the deployed database")
	return c
}

// importCommand loads a dump from stdin in the database's primary, as the
// app's role (so the app owns what the dump creates) and in one
// transaction (so a dump that fails leaves nothing behind).
func importCommand(custom bool) []string {
	if custom {
		return []string{"pg_restore", "-d", appDB, "--role=" + appDBRole, "--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error"}
	}
	return []string{"psql", "-X", "-q", "-d", appDB, "-v", "ON_ERROR_STOP=1", "--single-transaction", "-c", "SET ROLE " + appDBRole, "-f", "-"}
}

// isCustomDump reports whether a dump's first bytes are pg_dump's custom
// format.
func isCustomDump(head []byte) bool { return strings.HasPrefix(string(head), customDumpMagic) }

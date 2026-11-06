// Package migrate applies an app's SQL migrations: the files in its
// migrations directory, each once, in name order, recorded in the table
// maxcloud_migrations.
//
// This file is the same in mcloud (internal/migrate) and in the Go SDK
// (functions/internal/migrate); a test in mcloud keeps the two identical.
// The TypeScript SDK (packages/functions/src/migrate.ts) does the same
// thing, and all of them are tested against testdata/migrate.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// Dir is where an app keeps its migrations, next to maxcloud.yaml.
	Dir = "migrations"
	// Table records the applied migrations. It isn't schema-qualified: it
	// is the one the connection's search_path finds (public.maxcloud_migrations
	// on a default connection), and a new one goes in the connection's
	// current schema, next to the app's own tables (a test schema's own).
	Table = "maxcloud_migrations"
	// LockKey is the session advisory lock a run holds while it applies
	// migrations: the ASCII bytes of "maxcloud".
	LockKey int64 = 0x6d6178636c6f7564
)

const createTable = `CREATE TABLE IF NOT EXISTS ` + Table + ` (
  name text PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

// File is one migration.
type File struct {
	Name     string // 0001_init.sql
	Checksum string // hex SHA-256 of the file's bytes
	SQL      string
}

// A migration's name: a number, an underscore, a description.
var nameRe = regexp.MustCompile(`^([0-9]+)_[A-Za-z0-9._-]+\.sql$`)

// Load reads the migrations in dir of fsys: its files ending in .sql, in
// name order. Other files and subdirectories are left alone. A missing dir
// is no migrations. Every .sql file's name must be a number, an underscore
// and a description (0001_init.sql), and the numbers must all have the same
// width, so that name order is number order.
func Load(fsys fs.FS, dir string) ([]File, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []File
	width, widthFrom := 0, ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := nameRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("%s/%s: name a migration NUMBER_DESCRIPTION.sql, e.g. 0001_init.sql", dir, e.Name())
		}
		if width == 0 {
			width, widthFrom = len(m[1]), e.Name()
		} else if len(m[1]) != width {
			return nil, fmt.Errorf("%s/%s and %s: give the numbers the same width (0009_, 0010_), so the files sort in the order they apply", dir, widthFrom, e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		files = append(files, File{Name: e.Name(), Checksum: hex.EncodeToString(sum[:]), SQL: string(b)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// State is where a migration stands.
type State string

const (
	Applied State = "applied"
	Pending State = "pending"
	// Changed: applied, but the file isn't what was applied any more.
	Changed State = "changed"
	// Missing: applied, but there's no file for it (an older version of
	// the app, or a deleted file).
	Missing State = "missing"
)

// Entry is one migration's state.
type Entry struct {
	Name      string
	State     State
	AppliedAt time.Time // when it's applied
}

type record struct {
	checksum string
	at       time.Time
}

// tableExists reports whether the search_path finds the table.
func tableExists(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, Table).Scan(&exists)
	return exists, err
}

// applied reads the table; no table is nothing applied.
func applied(ctx context.Context, conn *pgx.Conn) (map[string]record, error) {
	exists, err := tableExists(ctx, conn)
	if err != nil {
		return nil, err
	}
	out := map[string]record{}
	if !exists {
		return out, nil
	}
	rows, err := conn.Query(ctx, `SELECT name, checksum, applied_at FROM `+Table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var r record
		if err := rows.Scan(&name, &r.checksum, &r.at); err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, rows.Err()
}

func entries(files []File, done map[string]record) []Entry {
	var out []Entry
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Name] = true
		r, ok := done[f.Name]
		switch {
		case !ok:
			out = append(out, Entry{Name: f.Name, State: Pending})
		case r.checksum != f.Checksum:
			out = append(out, Entry{Name: f.Name, State: Changed, AppliedAt: r.at})
		default:
			out = append(out, Entry{Name: f.Name, State: Applied, AppliedAt: r.at})
		}
	}
	for name, r := range done {
		if !seen[name] {
			out = append(out, Entry{Name: name, State: Missing, AppliedAt: r.at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status compares files with what the database has applied. It changes
// nothing and takes no lock.
func Status(ctx context.Context, conn *pgx.Conn, files []File) ([]Entry, error) {
	done, err := applied(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", Table, err)
	}
	return entries(files, done), nil
}

// ErrChanged is wrapped by the error for a migration file that changed
// after it was applied.
var ErrChanged = errors.New("changed after it was applied")

func check(files []File, done map[string]record) error {
	for _, f := range files {
		if r, ok := done[f.Name]; ok && r.checksum != f.Checksum {
			return fmt.Errorf("%s/%s %w (%s): put the change in a new migration instead. If the edit changes nothing the database needs (a comment, whitespace), record it: UPDATE %s SET checksum = '%s' WHERE name = '%s'",
				Dir, f.Name, ErrChanged, r.at.UTC().Format("2006-01-02 15:04 MST"), Table, f.Checksum, f.Name)
		}
	}
	return nil
}

// Apply applies the pending migrations in name order, each in its own
// transaction together with its row in the table, so a migration that
// fails leaves nothing behind. It refuses to apply anything if an applied
// migration's file changed. Migrations the database has applied but files
// doesn't have are left alone. Only one run applies at a time (an advisory
// lock); when nothing is pending it takes no lock and creates nothing.
// log, if set, hears what it's doing. It returns the names it applied.
func Apply(ctx context.Context, conn *pgx.Conn, files []File, log func(string)) ([]string, error) {
	if log == nil {
		log = func(string) {}
	}
	done, err := applied(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", Table, err)
	}
	if err := check(files, done); err != nil {
		return nil, err
	}
	if !anyPending(files, done) {
		return nil, nil
	}

	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, LockKey).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		log("waiting for another migration run to finish")
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, LockKey); err != nil {
			return nil, err
		}
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, LockKey)

	// Only when the search_path finds none: CREATE TABLE IF NOT EXISTS
	// would make a second, empty one in an earlier schema (a "$user"
	// schema made after the app first migrated into public).
	if exists, err := tableExists(ctx, conn); err != nil {
		return nil, err
	} else if !exists {
		if _, err := conn.Exec(ctx, createTable); err != nil {
			return nil, fmt.Errorf("creating %s: %w", Table, err)
		}
	}
	// Another run may have applied some while this one waited.
	if done, err = applied(ctx, conn); err != nil {
		return nil, fmt.Errorf("reading %s: %w", Table, err)
	}
	if err := check(files, done); err != nil {
		return nil, err
	}
	var names []string
	for _, f := range files {
		if _, ok := done[f.Name]; ok {
			continue
		}
		log("applying " + f.Name)
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// No arguments: the simple protocol, so a file may hold many
			// statements.
			if _, err := tx.Exec(ctx, f.SQL); err != nil {
				return fileError(f, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO `+Table+` (name, checksum) VALUES ($1, $2)`, f.Name, f.Checksum)
			return err
		})
		if err != nil {
			return names, err
		}
		names = append(names, f.Name)
	}
	return names, nil
}

func anyPending(files []File, done map[string]record) bool {
	for _, f := range files {
		if _, ok := done[f.Name]; !ok {
			return true
		}
	}
	return false
}

// fileError names the file, and the line when Postgres says where.
func fileError(f File, err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Position > 0 {
		r := []rune(f.SQL)
		line := 1 + strings.Count(string(r[:min(int(pe.Position)-1, len(r))]), "\n")
		return fmt.Errorf("%s/%s, line %d: %w", Dir, f.Name, line, err)
	}
	return fmt.Errorf("%s/%s: %w", Dir, f.Name, err)
}

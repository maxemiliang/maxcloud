package migrate

// Like migrate.go, this file is the same in mcloud and the Go SDK. The
// cases are the repo's testdata/migrate, shared with the TypeScript SDK.
// The database tests need a Postgres:
// TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

type cases struct {
	Table   string `json:"table"`
	LockKey string `json:"lockKey"`
	Load    []struct {
		Dir   string `json:"dir"`
		Files []struct {
			Name     string `json:"name"`
			Checksum string `json:"checksum"`
		} `json:"files"`
		Error string `json:"error"`
	} `json:"load"`
	Apply []struct {
		Dir          string      `json:"dir"`
		StatusBefore []wantEntry `json:"statusBefore"`
		Applied      []string    `json:"applied"`
		Error        string      `json:"error"`
		TablesAbsent []string    `json:"tablesAbsent"`
	} `json:"apply"`
}

type wantEntry struct {
	Name  string `json:"name"`
	State State  `json:"state"`
}

// testdata is the repo's testdata/migrate, found from this package up.
func testdata(t *testing.T) (string, *cases) {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "testdata", "migrate")
		if b, err := os.ReadFile(filepath.Join(p, "cases.json")); err == nil {
			var c cases
			if err := json.Unmarshal(b, &c); err != nil {
				t.Fatal(err)
			}
			return p, &c
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no testdata/migrate/cases.json above this package")
		}
		dir = parent
	}
}

func TestConstants(t *testing.T) {
	_, c := testdata(t)
	if c.Table != Table || c.LockKey != strconv.FormatInt(LockKey, 10) {
		t.Fatalf("table %s, lock key %d; cases.json has %s, %s", Table, LockKey, c.Table, c.LockKey)
	}
}

func TestLoad(t *testing.T) {
	root, c := testdata(t)
	for _, lc := range c.Load {
		t.Run(lc.Dir, func(t *testing.T) {
			files, err := Load(os.DirFS(root), lc.Dir)
			if lc.Error != "" {
				if err == nil || !strings.Contains(err.Error(), lc.Error) {
					t.Fatalf("error %v, want one containing %q", err, lc.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != len(lc.Files) {
				t.Fatalf("%d files, want %d", len(files), len(lc.Files))
			}
			for i, f := range files {
				if f.Name != lc.Files[i].Name || f.Checksum != lc.Files[i].Checksum {
					t.Errorf("file %d: %s %s, want %s %s", i, f.Name, f.Checksum, lc.Files[i].Name, lc.Files[i].Checksum)
				}
			}
		})
	}
}

// testConn is a connection to a new, empty database, dropped afterwards.
func testConn(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the migration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	rand.Read(b)
	name := "migrate_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close(ctx)
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	return conn
}

func TestApplyCases(t *testing.T) {
	root, c := testdata(t)
	conn := testConn(t)
	ctx := context.Background()
	for i, step := range c.Apply {
		files, err := Load(os.DirFS(root), step.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if step.StatusBefore != nil {
			st, err := Status(ctx, conn, files)
			if err != nil {
				t.Fatal(err)
			}
			var got []wantEntry
			for _, e := range st {
				got = append(got, wantEntry{e.Name, e.State})
			}
			if !reflect.DeepEqual(got, step.StatusBefore) {
				t.Errorf("step %d (%s): status %v, want %v", i, step.Dir, got, step.StatusBefore)
			}
		}
		names, err := Apply(ctx, conn, files, nil)
		if step.Error != "" {
			if err == nil || !strings.Contains(err.Error(), step.Error) {
				t.Fatalf("step %d (%s): error %v, want one containing %q", i, step.Dir, err, step.Error)
			}
			if step.Error == "migrations/0001_init.sql changed after it was applied" && !errors.Is(err, ErrChanged) {
				t.Errorf("step %d: %v isn't ErrChanged", i, err)
			}
		} else if err != nil {
			t.Fatalf("step %d (%s): %v", i, step.Dir, err)
		}
		if step.Applied != nil && !reflect.DeepEqual(names, step.Applied) && len(names)+len(step.Applied) > 0 {
			t.Errorf("step %d (%s): applied %v, want %v", i, step.Dir, names, step.Applied)
		}
		for _, table := range step.TablesAbsent {
			var exists bool
			if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				t.Errorf("step %d (%s): table %s exists after the migration failed", i, step.Dir, table)
			}
		}
	}
}

func TestApplyConcurrently(t *testing.T) {
	root, _ := testdata(t)
	first := testConn(t)
	ctx := context.Background()
	cfg := first.Config()
	files, err := Load(os.DirFS(root), "v2")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([][]string, 4)
	errs := make([]error, 4)
	for i := range results {
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Apply(ctx, conn, files, nil)
		}()
	}
	wg.Wait()
	count := map[string]int{}
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		for _, n := range results[i] {
			count[n]++
		}
	}
	for _, f := range files {
		if count[f.Name] != 1 {
			t.Errorf("%s applied %d times", f.Name, count[f.Name])
		}
	}
}

func TestNothingPendingCreatesNothing(t *testing.T) {
	conn := testConn(t)
	names, err := Apply(context.Background(), conn, nil, nil)
	if err != nil || names != nil {
		t.Fatalf("%v, %v", names, err)
	}
	var exists bool
	if err := conn.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", Table).Scan(&exists); err != nil || exists {
		t.Fatalf("table exists: %v, %v", exists, err)
	}
}

// tableSchema is the schema that has a table named Table, or "".
func tableSchema(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var schemas []string
	rows, err := conn.Query(context.Background(), "SELECT table_schema FROM information_schema.tables WHERE table_name = $1 ORDER BY 1", Table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		schemas = append(schemas, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(schemas, ",")
}

// The table follows the connection's search_path, like the app's own
// tables: a test schema without public on its path gets its own record.
func TestTableFollowsTheSearchPath(t *testing.T) {
	root, _ := testdata(t)
	conn := testConn(t)
	ctx := context.Background()
	v1, err := Load(os.DirFS(root), "v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"test_a", "test_b"} {
		if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema+"; SET search_path = "+schema); err != nil {
			t.Fatal(err)
		}
		names, err := Apply(ctx, conn, v1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 2 {
			t.Errorf("%s: applied %v, want both", schema, names)
		}
	}
	if got := tableSchema(t, conn); got != "test_a,test_b" {
		t.Errorf("tables in %q, want test_a,test_b", got)
	}
}

// An app migrated into public keeps using public.maxcloud_migrations, even
// when a schema earlier on its search_path (a "$user" schema) appears later.
func TestExistingTableInPublicIsKept(t *testing.T) {
	root, _ := testdata(t)
	conn := testConn(t)
	ctx := context.Background()
	v1, _ := Load(os.DirFS(root), "v1")
	v2, _ := Load(os.DirFS(root), "v2")
	if _, err := Apply(ctx, conn, v1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA shadow; SET search_path = shadow, public"); err != nil {
		t.Fatal(err)
	}
	names, err := Apply(ctx, conn, v2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "0003_more.sql" {
		t.Errorf("applied %v, want only 0003_more.sql", names)
	}
	if got := tableSchema(t, conn); got != "public" {
		t.Errorf("tables in %q, want public", got)
	}
}

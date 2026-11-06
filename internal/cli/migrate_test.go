package cli

// Needs a Postgres: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/maxemiliang/maxcloud/internal/functions"
)

func TestLoadMigrationsDefaultsToProjectDir(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ProjectFile), []byte("app: shop\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "migrations"), 0o755)
	os.WriteFile(filepath.Join(root, "migrations", "0001_init.sql"), []byte("SELECT 1;\n"), 0o644)
	sub := filepath.Join(root, "src")
	os.MkdirAll(sub, 0o755)
	t.Chdir(sub)
	files, where, err := loadMigrations("")
	if err != nil || len(files) != 1 || !strings.HasSuffix(where, "migrations") {
		t.Fatalf("%v %q %v", files, where, err)
	}
	t.Chdir(t.TempDir())
	if _, _, err := loadMigrations(""); err == nil || !strings.Contains(err.Error(), "no migrations directory") {
		t.Errorf("no directory: %v", err)
	}
	if _, _, err := loadMigrations("nope"); err == nil || !strings.Contains(err.Error(), "--dir nope") {
		t.Errorf("--dir nope: %v", err)
	}
}

func TestDBMigrateDatabaseURL(t *testing.T) {
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
	name := "mcloud_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dbURL := u.String()
	dir, _ := filepath.Abs("../../testdata/migrate/v2")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		g := &globals{in: strings.NewReader(""), out: &out, errOut: &out, plain: true}
		root := newRoot(g)
		root.SetArgs(append([]string{"db", "migrate", "--dir", dir, "--database-url", dbURL}, args...))
		err := root.ExecuteContext(ctx)
		return out.String(), err
	}
	out, err := run("--dry-run")
	if err != nil || strings.Count(out, "pending") < 3 {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Applied 3 migration(s)") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Up to date") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	edited, _ := filepath.Abs("../../testdata/migrate/edited")
	if out, err := run("--dir", edited); err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf("edited: %v\n%s", err, out)
	}
}

func TestImageMigrationsWarning(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ProjectFile), []byte("app: shop\n"), 0o644)
	t.Chdir(root)
	parse := func(js string) *functions.Description {
		d, err := functions.Parse([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	old := parse(`{"sdk":"@max-cloud/functions","version":"0.4.0","functions":[]}`)
	if w := imageMigrationsWarning(old); w != "" {
		t.Errorf("no migrations/: %q", w)
	}
	os.MkdirAll("migrations", 0o755)
	os.WriteFile("migrations/0001_init.sql", []byte("SELECT 1;\n"), 0o644)
	os.WriteFile("migrations/0002_more.sql", []byte("SELECT 2;\n"), 0o644)
	if w := imageMigrationsWarning(old); !strings.Contains(w, "doesn't apply migrations") {
		t.Errorf("old SDK: %q", w)
	}
	partial := parse(`{"sdk":"@max-cloud/functions","version":"0.5.0","functions":[],"migrations":["0001_init.sql"]}`)
	if w := imageMigrationsWarning(partial); !strings.Contains(w, "doesn't have migrations/0002_more.sql") {
		t.Errorf("partial: %q", w)
	}
	all := parse(`{"sdk":"@max-cloud/functions","version":"0.5.0","functions":[],"migrations":["0001_init.sql","0002_more.sql"]}`)
	if w := imageMigrationsWarning(all); w != "" {
		t.Errorf("all there: %q", w)
	}
}

// drizzle-kit's folder has the same file names; drizzle applies it itself.
func TestDrizzleKitMigrationsAreLeftAlone(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ProjectFile), []byte("app: shop\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "migrations", "meta"), 0o755)
	os.WriteFile(filepath.Join(root, "migrations", "0000_init.sql"), []byte("CREATE TABLE a (id int);\n"), 0o644)
	os.WriteFile(filepath.Join(root, "migrations", "meta", "_journal.json"), []byte("{}"), 0o644)
	t.Chdir(root)
	if _, _, err := loadMigrations(""); err == nil || !strings.Contains(err.Error(), "drizzle-kit") {
		t.Errorf("mcloud db migrate on a drizzle-kit folder: %v", err)
	}
	if w := imageMigrationsWarning(&functions.Description{SDK: "@max-cloud/functions", Version: "0.4.0"}); w != "" {
		t.Errorf("deploy warning for a drizzle-kit folder: %q", w)
	}
}

package functions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

// fixture is testdata/migrate/<dir> (shared with mcloud and the
// TypeScript SDK) as an embed-like FS with a migrations directory.
func fixture(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	paths, _ := filepath.Glob(filepath.Join("..", "testdata", "migrate", dir, "*.sql"))
	if len(paths) == 0 {
		t.Fatalf("no fixture %s", dir)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fsys["migrations/"+filepath.Base(p)] = &fstest.MapFile{Data: b}
	}
	return fsys
}

func TestDescribeListsMigrations(t *testing.T) {
	t.Cleanup(reset)
	HTTPFunc("api", nil)
	describeMigrations := func() (int, []string, string) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--describe"}, env(nil), &out, &errOut)
		var d struct {
			Migrations []string `json:"migrations"`
		}
		json.Unmarshal(out.Bytes(), &d)
		return code, d.Migrations, errOut.String()
	}
	if code, m, _ := describeMigrations(); code != 0 || m == nil || len(m) != 0 {
		t.Errorf("without migrations: %d %#v", code, m)
	}
	Migrations(fixture(t, "v2"))
	if code, m, _ := describeMigrations(); code != 0 || strings.Join(m, " ") != "0001_init.sql 0002_items.sql 0003_more.sql" {
		t.Errorf("v2: %d %v", code, m)
	}
	Migrations(fstest.MapFS{"migrations/init.sql": {Data: []byte("SELECT 1;")}})
	if code, _, errOut := describeMigrations(); code != 1 || !strings.Contains(errOut, "0001_init.sql") {
		t.Errorf("bad name: %d %s", code, errOut)
	}
}

func TestMigrationsNeedDatabase(t *testing.T) {
	t.Cleanup(reset)
	Schedule("nightly", "0 3 * * *", func(context.Context) error { return nil })
	Migrations(fixture(t, "v1"))
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--target", "nightly"}, env(nil), io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "DATABASE_URL isn't set") {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"--target", "nightly"}, env(map[string]string{"MAXCLOUD_MIGRATE": "0"}), io.Discard, &errOut); code != 0 {
		t.Errorf("MAXCLOUD_MIGRATE=0: exit %d: %s", code, errOut.String())
	}
}

func TestStartAppliesMigrations(t *testing.T) {
	t.Cleanup(reset)
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the migration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	rand.Read(b)
	name := "sdk_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	dbURL := u.String()

	var users int
	Schedule("count-users", "0 3 * * *", func(ctx context.Context) error {
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		return conn.QueryRow(ctx, "SELECT count(*) FROM users WHERE name = 'Ada'").Scan(&users)
	})
	Migrations(fixture(t, "v2"))
	var errOut bytes.Buffer
	e := env(map[string]string{"DATABASE_URL": dbURL})
	if code := run(ctx, []string{"--target", "count-users"}, e, io.Discard, &errOut); code != 0 || users != 1 {
		t.Fatalf("exit %d, users %d: %s", code, users, errOut.String())
	}
	if !strings.Contains(errOut.String(), "migrations: applied 3") {
		t.Errorf("logs: %s", errOut.String())
	}
	errOut.Reset()
	if code := run(ctx, []string{"--target", "count-users"}, e, io.Discard, &errOut); code != 0 || strings.Contains(errOut.String(), "applied") {
		t.Errorf("second start: exit %d: %s", code, errOut.String())
	}
	// A changed migration stops the process before the function runs.
	Migrations(fixture(t, "edited"))
	errOut.Reset()
	if code := run(ctx, []string{"--target", "count-users"}, e, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "changed after it was applied") {
		t.Errorf("edited: exit %d: %s", code, errOut.String())
	}
}

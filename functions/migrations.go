package functions

import (
	"context"
	"fmt"
	"io/fs"
	"log"

	"github.com/jackc/pgx/v5"

	"github.com/maxemiliang/maxcloud/functions/internal/migrate"
)

var migrationsFS fs.FS

// Migrations gives Start the app's SQL migrations: fsys holds a migrations
// directory of NUMBER_DESCRIPTION.sql files (0001_init.sql, ...), the way
// //go:embed gives one:
//
//	//go:embed migrations
//	var migrations embed.FS
//
//	func main() {
//		functions.Migrations(migrations)
//		...
//		functions.Start()
//	}
//
// Before it runs a function, Start applies the ones the database hasn't
// had, each once, in name order, the same way mcloud db migrate does (and
// recorded in the same table, maxcloud_migrations). Only one process
// applies them at a time; the others wait, so no function runs on an older
// schema than its code expects. A migration that fails stops the process.
// Dev mode leaves them to mcloud dev, which applies them before it starts
// the app. MAXCLOUD_MIGRATE=0 in the environment turns it off.
func Migrations(fsys fs.FS) {
	mu.Lock()
	defer mu.Unlock()
	migrationsFS = fsys
}

// migrationFiles are the app's migrations (none without Migrations).
func migrationFiles() ([]migrate.File, error) {
	mu.Lock()
	fsys := migrationsFS
	mu.Unlock()
	if fsys == nil {
		return nil, nil
	}
	return migrate.Load(fsys, migrate.Dir)
}

// migrationNames are what the description lists: never nil, so mcloud can
// tell this SDK from one without migrations.
func migrationNames() ([]string, error) {
	files, err := migrationFiles()
	names := []string{}
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names, err
}

// applyMigrations applies the pending migrations before a function runs.
func applyMigrations(ctx context.Context, getenv func(string) string, logger *log.Logger) error {
	if getenv("MAXCLOUD_MIGRATE") == "0" {
		return nil
	}
	files, err := migrationFiles()
	if err != nil || len(files) == 0 {
		return err
	}
	url := getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("migrations: the app has %d, but DATABASE_URL isn't set (db: true in maxcloud.yaml)", len(files))
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	names, err := migrate.Apply(ctx, conn, files, func(m string) { logger.Printf("migrations: %s", m) })
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if len(names) > 0 {
		logger.Printf("migrations: applied %d", len(names))
	}
	return nil
}

# Databases

An app gets a Postgres database (the way a Worker gets D1, but Postgres)
from `db:` in `maxcloud.yaml`. The controller runs it with CloudNativePG,
backs it up continuously to the bucket `mc-shop--db`, and restores it from
there by itself if its cluster is lost. Every function gets
`DATABASE_URL`. An app set up before this may have a `database.yaml` (what
mcloud used to write for queue functions, with no backups): it keeps
working, but the controller won't take it over. Moving it is a dump and
restore (homelab `docs/runbooks/move-database-to-registry.md`).

```fish
mcloud db migrate                   # apply migrations/*.sql (see Migrations)
mcloud db psql                      # psql in it (--branch NAME for a branch)
mcloud db proxy                     # on localhost, with a DATABASE_URL to use
mcloud db restore --at 15m          # as it was 15 minutes ago, in place
mcloud db branch create try         # a throwaway copy (--at for a point in time)
```

`restore` and `branch` restore from the backups, so they reach back as far
as the retention (7 days by default).

## Migrations

SQL migrations go in `migrations/` in the app's folder (next to
`maxcloud.yaml`, or in `source:`), one file per
change, named `NUMBER_DESCRIPTION.sql` with numbers of one width:

```
migrations/0001_init.sql
migrations/0002_add_orders.sql
```

Each file is applied once, in name order, in its own transaction together
with its row in the table `maxcloud_migrations` (name, SHA-256 checksum,
time applied): the one the connection's `search_path` finds, else a new
one in its current schema, which is `public` on maxcloud. A file that fails leaves nothing behind. Don't put `BEGIN`
or `COMMIT` in them, and statements that can't run in a transaction
(`CREATE INDEX CONCURRENTLY`) can't be migrations. A file edited after it
was applied stops every run until you put the change in a new file
instead. A file that sorts before one already applied (from a merged
branch) is still applied. Only one run applies at a time (a Postgres
advisory lock); the others wait.

They're applied in three places, all the same way and to the same table:

- **On deploy, by the app itself.** Before it runs a function, the SDK's
  runtime applies the pending migrations to `DATABASE_URL`. A new Knative
  revision takes no traffic until it has, so new code never serves an old
  schema, and a migration that fails stops the pod while the old revision
  keeps serving. TypeScript apps read `./migrations` (buildpacks keep it
  in the image; with a Dockerfile, copy it in next to `package.json`); Go
  apps embed it (`//go:embed migrations`, `functions.Migrations(...)`).
  When the image doesn't have every file in `migrations/`, the deploy
  warns. Keep them quick: every starting pod waits for them.
- **`mcloud dev`** applies them to its database before it starts the app
  (`--migrate=false` doesn't).
- **`mcloud db migrate`**, by hand: to the deployed database through a
  port-forward as the app's user (it asks first), a branch (`--branch`),
  or any Postgres (`--database-url`). `--dry-run` lists what's applied and
  what's pending.

```fish
mcloud db migrate --dry-run             # applied, pending, changed
mcloud db migrate --branch try          # try them on a branch first
```

A `migrations/` that drizzle-kit wrote (it has `meta/_journal.json`) is
drizzle's: none of the three touch it, and the app applies it with
drizzle-kit or drizzle's migrator.

Coming from D1: the files `wrangler d1 migrations create` writes
(`0000_init.sql`) keep their names, but their SQL has to become Postgres
(`INTEGER PRIMARY KEY AUTOINCREMENT` is `bigserial PRIMARY KEY`, and so
on).

# Databases

An app gets a Postgres database (the way a Worker gets D1, but Postgres)
from `db:` in `maxcloud.yaml`. The controller runs it with CloudNativePG,
backs it up continuously to the bucket `mc-shop--db`, and restores it from
there by itself if its cluster is lost. Every function gets
`DATABASE_URL`. An app set up before this may have a `database.yaml` (what
mcloud used to write for queue functions, with no backups): it keeps
working, but the controller won't take it over. Moving it is a dump and
restore
([move-database-to-registry](../platform/docs/runbooks/move-database-to-registry.md)).

```fish
mcloud db migrate                   # apply migrations/*.sql (see Migrations)
mcloud db psql                      # psql in it (--branch NAME for a branch)
mcloud db proxy                     # on localhost, with a DATABASE_URL to use
mcloud db restore --at 15m          # as it was 15 minutes ago, in place
mcloud db branch create try         # a throwaway copy (--at for a point in time)
mcloud db dump                      # pg_dump to a file here (--schema-only, --format custom)
mcloud db import old.sql            # load a dump (--branch try: into a branch first)
```

`restore` and `branch` restore from the backups, so they reach back as far
as the retention (7 days by default).

An [environment](environments-and-previews.md) has a database of its own,
empty to start with and backed up like the app's (`mcloud db psql --env
staging`); a preview, with `previews.db`, an empty one deleted with it.
Neither is a copy of production's: to try a change on production's data,
branch it.

## Moving data in and out

`mcloud db dump` runs `pg_dump` in the database and writes the result to a
file on your machine: SQL by default, with no owners or grants in it, so it
loads into any Postgres. `mcloud db import FILE` loads one (SQL, or a
`pg_dump -Fc` archive), as the app's own user and in one transaction: a
dump that fails halfway leaves the database as it was. It adds to what's
there, and asks before it writes to the deployed database.

```fish
mcloud db dump --schema-only -o schema.sql
mcloud db branch create try; and mcloud db import old.sql --branch try
pg_dump --no-owner --no-privileges $OLD_DATABASE_URL | mcloud db import -
```

A dump isn't the backup. The database backs itself up continuously
(`restore`, `branch`); a dump is one moment, for taking data somewhere
else.

## Connections

Postgres takes 100 connections, and some are its own. Every instance of a
function keeps a pool: the SDKs' `db()` holds at most 4, and a queue worker
one more per job it works on at once, plus one. So what an app can hold is
its functions' `maxInstances` added up, times that. Past about 80, a deploy
warns: queries would start failing with `too many clients already`. Lower
`maxInstances` (or a queue's `concurrency`) on the functions that use the
database. There's no connection pooler in front of the database.

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

A deploy that's rolled back (`mcloud rollback`) leaves the schema as it
is: nothing undoes a migration. mcloud warns when the database has
migrations the old deploy doesn't know, so check that the old code works
with them; if it can't, deploy a fix instead. `mcloud db restore --at`
brings back the data from before them, and loses what was written since.

A `migrations/` that drizzle-kit wrote (it has `meta/_journal.json`) is
drizzle's: none of the three touch it, and the app applies it with
drizzle-kit or drizzle's migrator.

Coming from D1: the files `wrangler d1 migrations create` writes
(`0000_init.sql`) keep their names, but their SQL has to become Postgres
(`INTEGER PRIMARY KEY AUTOINCREMENT` is `bigserial PRIMARY KEY`, and so
on).

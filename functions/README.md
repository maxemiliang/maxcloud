# functions (Go)

Write functions for maxcloud, the homelab's run platform, in Go. It's the Go
twin of [`@max-cloud/functions`](../packages/functions): the same kinds of
function, the same queue table, and the same contract with `mcloud`, so one
app's Node.js and Go parts can share a queue.

```fish
go get github.com/maxemiliang/maxcloud/functions@latest
```

```go
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/maxemiliang/maxcloud/functions"
)

func main() {
	// A Knative service: scales to zero, starts on the first request.
	functions.HTTPFunc("api", api, functions.Public())
	// A CronJob.
	functions.Schedule("nightly", "0 3 * * *", nightly, functions.Timeout(10*time.Minute))
	// A worker that KEDA starts when the queue has jobs.
	functions.Queue("resize", "images", resize, functions.Concurrency(2))
	functions.Start()
}

func api(w http.ResponseWriter, r *http.Request) {
	id, err := functions.Enqueue(r.Context(), "images", map[string]string{"key": r.URL.Query().Get("key")})
	...
}

func nightly(ctx context.Context) error { ... }

func resize(ctx context.Context, job *functions.Job) error {
	var p struct{ Key string }
	if err := job.Decode(&p); err != nil {
		return err
	}
	...
}
```

The name you register becomes the function's: its service, CronJob or
worker. Use at most 40 lowercase letters, digits and dashes, starting with
a letter; a bad name, a name registered twice or a bad cron schedule
panics. `functions.HTTP(name, handler)` takes any `http.Handler` (a
router, say); `HTTPFunc` takes a handler function. A handler that panics
answers 500.

## Options

| Option | Applies to | Default |
| --- | --- | --- |
| `Public()` | HTTP | private: cluster-local |
| `MinInstances(n)` | HTTP | 0 (scale to zero) |
| `MaxInstances(n)` | HTTP, queue | 3 |
| `Concurrency(n)` | HTTP: requests per instance; queue: jobs per instance | 0 (no limit) / 1 |
| `Timeout(d)` | HTTP request (≤ 10m), scheduled run, job | 5m / 1h / 5m |
| `Memory(q)`, `CPU(q)` | all | the namespace's defaults |
| `TimeZone(tz)` | schedule | UTC |
| `Retries(n)` | schedule | 0 |
| `MaxAttempts(n)` | queue | 5 |
| `Backoff(d)` | queue: first retry's delay, doubled each time | 10s |
| `Proxied()` | HTTP: the public hostname behind Cloudflare (WAF, caching, Access); needs `Public()` | off |
| `HealthPath(p)` | HTTP: a path that answers 2xx when an instance can take requests (readiness, and a liveness probe that restarts a stuck one) | none |
| `MemoryLimit(q)` | all: memory above `Memory` (the request), for spikes | `Memory` |
| `User(id)` | all: run as this user id and group, for an image without a non-root `USER` | the image's |
| `ReadOnlyRootFilesystem()` | all: write only to volumes | off |
| `Volume(path, sizeLimit)`, `MemoryVolume(path, sizeLimit)` | all: a scratch directory, empty at each start; `MemoryVolume` is in RAM and counts against the memory limit. Knative doesn't allow `/tmp`: point `TMPDIR` at a volume | none |

Options that don't apply to a function's kind are ignored. Timeouts are
rounded up to whole seconds.

## Queues

`functions.Enqueue(ctx, queue, payload)` adds a job and returns its id;
the payload is stored as JSON, and the handler reads it with
`job.Decode(&v)`. A queue's name is lowercase letters, digits, dashes and
underscores. Jobs live in the app's Postgres database (`$DATABASE_URL`;
the app gets one with `db: true` in its `maxcloud.yaml`), in a table the
SDK creates (`maxcloud_jobs`).

- **Retries:** a job whose handler returns an error, panics, or passes its
  timeout is retried, and so is one whose worker died, once its lease (the
  timeout plus 30 seconds) runs out. Jobs can run more than once, so make
  handlers safe to repeat.
- **Return when `ctx` is done.** A handler that ignores it keeps running in
  the background after its job has been counted as failed.
- **Shutting down:** on SIGTERM the worker takes no new jobs, gives the
  ones in hand 25 seconds, then cancels their `ctx` and puts them back
  without counting the attempt.
- **Enqueue in your own transaction** with `functions.In(tx)` (a `pgx.Tx`,
  `*pgx.Conn` or pool), and the job exists only if the transaction
  commits. The table must exist by then (a worker or an earlier `Enqueue`
  creates it).
- **Delay a job** with `functions.Delay(d)`.
- **Cleanup:** a worker deletes jobs that finished more than 7 days ago
  when it starts. Failed jobs stay, with the error in `last_error`.

## Database

With `db: true` in the app's `maxcloud.yaml`, every function gets
`$DATABASE_URL`. `functions.Pool(ctx)` is a pgx pool on it, made on first
use and shared with `Enqueue` and the KV functions:

```go
p, err := functions.Pool(ctx)
err = p.QueryRow(ctx, "SELECT name FROM users WHERE id = $1", id).Scan(&name)
```

It returns an error if `$DATABASE_URL` isn't set.

### Migrations

Put SQL migrations in `migrations/` at the app's root, named
`NUMBER_DESCRIPTION.sql` with numbers of one width: `0001_init.sql`,
`0002_add_orders.sql`. Embed them, since the built image has the binary and
not the source, and hand them to the runtime before `Start`:

```go
//go:embed migrations
var migrations embed.FS

func main() {
	functions.Migrations(migrations)
	functions.HTTPFunc("api", api)
	functions.Start()
}
```

Before `Start` runs a function, it applies the ones the database hasn't
had: each once, in name order, in its own transaction, recorded in the
table `public.maxcloud_migrations`. Only one process applies them at a
time; the others wait, so no function starts on an older schema than its
code expects. A migration that fails, a file edited after it was applied,
or migrations without a `$DATABASE_URL` stop the process (exit 1). A
migration the database has but the binary doesn't is left alone.
`MAXCLOUD_MIGRATE=0` in the environment turns it off. `--describe` lists
the embedded files, and the deploy warns when `migrations/` has one the
binary doesn't.

Dev mode leaves them to `mcloud dev`, which applies them from
`migrations/` before it starts the app; `mcloud db migrate` applies them by
hand (maxcloud's README, "Migrations", has the rules).

## Buckets

An app's S3 buckets are listed in its `maxcloud.yaml`
(`buckets: [uploads, assets:public]`); the maxcloud controller creates
them as `mc-<app>--<bucket>` and gives every function the app's
credentials as `AWS_*` environment variables, which the AWS SDK reads by
itself. `MAXCLOUD_APP`, the app's name, is set on every function.
`functions.BucketName("uploads")` returns the full name (or an error
without `MAXCLOUD_APP`). Each bucket's full name is also in
`MAXCLOUD_BUCKET_<NAME>` (`MAXCLOUD_BUCKET_UPLOADS`), for libraries
configured by environment; a pod started before the bucket existed
doesn't have it.

```go
cfg, err := config.LoadDefaultConfig(ctx)
s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
bucket, err := functions.BucketName("uploads") // "mc-shop--uploads"
_, err = s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: aws.String("a.txt"), Body: r})
```

### Public buckets

A bucket listed as `assets:public` can be read by anyone (not listed) at
`https://<bucket>--<app>.gronblom.dev/<key>`, behind Cloudflare, which
caches it. Writing still needs the app's credentials:

```go
bucket, _ := functions.BucketName("assets")
key := "avatars/" + userID + "." + hash + ".png" // new content, new key: the CDN caches
_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
	Bucket: &bucket, Key: &key, Body: png,
	ContentType:  aws.String("image/png"),
	CacheControl: aws.String("public, max-age=31536000, immutable"),
})
url := "https://assets--" + os.Getenv("MAXCLOUD_APP") + ".gronblom.dev/" + key
```

The objects are also readable straight from Object Storage, at
`<AWS_ENDPOINT_URL_S3>/<full bucket name>/<key>` (not cached). Removing
`:public` makes the bucket private again; its objects stay.

## Key-value storage

`KVGet`, `KVSet`, `KVDelete` and `KVList` are a key-value store (like
Workers KV) in the app's Postgres database (`DATABASE_URL`; the app needs
`db: true` in its `maxcloud.yaml`). Values are JSON; reads see writes
right away. Keys are 1 to 512 bytes.

```go
err := functions.KVSet(ctx, "session:42", session, time.Hour) // 0: no expiry
found, err := functions.KVGet(ctx, "session:42", &session)   // false if missing or expired
existed, err := functions.KVDelete(ctx, "session:42")
keys, next, err := functions.KVList(ctx, "session:", "", 100)
```

`KVList` gives keys in order after the cursor (`""` to start), up to the
limit (0: 100; at most 1000), and the cursor for the next page, `""` when
there are no more. It's one table, `maxcloud_kv`, made on first use.

## Secrets

`mcloud secrets set STRIPE_KEY` (in the app's repo) stores a secret; every
function gets it as an environment variable. `functions.Secret("STRIPE_KEY")`
reads it, with an error that says how to set it when it's missing.

## Running

`functions.Start()` runs the function named in `$FUNCTION_TARGET` (or
`--target`):

- **HTTP:** a server on `$PORT` (or `--port`, else 8080).
- **Schedule:** one run, then it exits 0, or 1 on an error.
- **Queue:** the worker loop.

First it applies the app's pending migrations (see Migrations). On SIGTERM
an HTTP server or worker finishes what's in flight (up to 25 seconds) and
exits 0; a scheduled run's `ctx` is cancelled. `Start` doesn't return: it
exits the process, so deferred calls in `main` don't run.

With `--describe` it prints the registered functions, their settings and
the embedded migrations as JSON instead. With `MAXCLOUD_DESCRIBE=1` in the
environment it prints the same as one line, `maxcloud:describe:{...}`.
That's how the deploy asks the built image: it runs it with that variable
and the image's own command, so the image has to run your binary, and
reads the line. Register functions in `main` before `Start`, and don't
connect to anything before that: describing runs without the database or
secrets. To see what a deploy would write:

```fish
go run . --describe | mcloud generate --from -
```

## Local development

Run `mcloud dev` in the app's repo. It starts a local Postgres (in Docker)
or port-forwards to a database branch, sets `DATABASE_URL`, `MAXCLOUD_APP`
and the secrets from `.env` (keep it out of git), and runs the app in dev
mode with `go run .`. It doesn't watch for changes: restart it yourself.

Dev mode is `go run . --dev`, or `MAXCLOUD_DEV=1` in the environment. It
runs the whole app in one process, with `MAXCLOUD_DEV=1` set:

- One server on 127.0.0.1, on `--port`, `$PORT` or 8080.
- `http://<name>.localhost:<port>/` reaches the HTTP function registered as
  `<name>`. The request goes through as it is (path, method, headers,
  body), so a router mounted at `/` works.
- `GET http://localhost:<port>/` lists the functions as JSON.
- `POST http://localhost:<port>/_run/<name>` runs a scheduled function once.
  It answers 200 `ok`, or 500 with the error. Schedules never run on their
  cron in dev mode.
- Queue workers run in the same process when `DATABASE_URL` is set. Without
  it they don't, and `Enqueue` fails as it would anywhere else.

At startup it prints each function and where to reach it. Ctrl-C finishes
the requests and jobs in flight, then exits.

To run a single function, the way its container does:

```fish
FUNCTION_TARGET=api go run .
```

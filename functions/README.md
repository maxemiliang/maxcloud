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
worker. Use at most 40 lowercase letters, digits and dashes.

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

## Queues

Jobs live in the app's Postgres database (`$DATABASE_URL`), in a table the
SDK creates (`maxcloud_jobs`).

- **Retries:** a job whose handler returns an error, panics, or passes its
  timeout is retried, and so is one whose worker died, once its lease runs
  out. Jobs can run more than once, so make handlers safe to repeat.
- **Return when `ctx` is done.** A handler that ignores it keeps running in
  the background after its job has been counted as failed.
- **Enqueue in your own transaction** with `functions.In(tx)`, and the job
  exists only if the transaction commits.
- **Delay a job** with `functions.Delay(d)`.

## Buckets

An app's S3 buckets come from the registry
(`mcloud apps set shop --buckets uploads,assets:public`); the maxcloud
controller creates them as `mc-<app>--<bucket>` and gives every function
the app's credentials as `AWS_*` environment variables, which the AWS SDK
reads by itself. `functions.BucketName("uploads")` returns the full name:

```go
cfg, err := config.LoadDefaultConfig(ctx)
s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
bucket, err := functions.BucketName("uploads") // "mc-shop--uploads"
_, err = s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: aws.String("a.txt"), Body: r})
```

## Key-value storage

`KVGet`, `KVSet`, `KVDelete` and `KVList` are a key-value store (like
Workers KV) in the app's Postgres database (`DATABASE_URL`; the app needs
one: `mcloud apps set <app> --db`). Values are JSON.

```go
err := functions.KVSet(ctx, "session:42", session, time.Hour) // 0: no expiry
found, err := functions.KVGet(ctx, "session:42", &session)
keys, next, err := functions.KVList(ctx, "session:", "", 100)
```

## Secrets

`mcloud secrets set STRIPE_KEY` (in the app's repo) stores a secret; every
function gets it as an environment variable. `functions.Secret("STRIPE_KEY")`
reads it, with an error that says how to set it when it's missing.

## Running

`functions.Start()` runs the function named in `$FUNCTION_TARGET` (or
`--target`):

- **HTTP:** a server on `$PORT`.
- **Schedule:** one run, then it exits 0, or 1 on an error.
- **Queue:** the worker loop.

On SIGTERM it finishes what's in flight (up to 25 seconds) and exits 0.

With `--describe`, or `MAXCLOUD_DESCRIBE=1` in the environment, it prints
the registered functions as JSON instead; that's what `mcloud` deploys from.
Register functions in `main` before `Start`, and don't connect to anything
before that, so describing stays fast.

To try a function locally:

```fish
FUNCTION_TARGET=api go run .
```

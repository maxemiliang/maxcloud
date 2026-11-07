# @max-cloud/functions

Write functions for maxcloud, the homelab's run platform (Knative and KEDA,
see [maxemiliang/maxcloud](https://github.com/maxemiliang/maxcloud)). Export
them from your app's entry module, and each one is deployed on its own:

```ts
import { enqueue, onQueue, onRequest, onSchedule } from "@max-cloud/functions";

// A Knative service: scales to zero, starts on the first request.
export const api = onRequest({ public: true }, async (req) => {
  const { key } = await req.json();
  await enqueue("images", { key });
  return Response.json({ queued: key }, { status: 202 });
});

// A CronJob.
export const nightly = onSchedule("0 3 * * *", async () => {
  // ...
});

// A worker that KEDA starts when the queue has jobs, and stops when it's empty.
export const resize = onQueue<{ key: string }>("images", async (job) => {
  console.log("resizing", job.payload.key, "attempt", job.attempt);
});
```

The export's name becomes the function's: `resizeImage` is deployed as
`resize-image` (an underscore becomes a dash too). It must start with a
letter and be at most 40 characters, and a function can't be the default
export.

## HTTP functions

`onRequest(handler)` or `onRequest(options, handler)`. The handler takes a
standard `Request` and returns a `Response`, so fetch-style frameworks work
as they are: `onRequest(app.fetch)` with Hono, for example. Its second
argument is `{ signal }`, aborted when the client goes away or the server
shuts down. A handler that throws answers 500. `req.url` is absolute: the
host is the request's `Host` header and the scheme `X-Forwarded-Proto`
(`http` or `https`). `X-Forwarded-Host` isn't used: a client can send its
own, and it reaches the app.

| Option | Default | |
| --- | --- | --- |
| `public` | `false` | Reachable from the internet at `https://<name>-<app>.gronblom.dev`. Otherwise cluster-local. |
| `minInstances` | `0` | Instances kept running when idle. |
| `maxInstances` | `3` | |
| `concurrency` | `0` | Requests one instance handles before another starts (0: no limit). |
| `timeoutSeconds` | `300` | Per request; at most 600 (Knative's limit, checked at deploy). |
| `memory` | the namespace's default | Memory the container requests and is limited to, e.g. `"256Mi"`. |
| `cpu` | the namespace's default | CPU the container requests, e.g. `"100m"`. |
| `proxied` | `false` | Put the public hostname behind Cloudflare (WAF, caching, Cloudflare Access). Needs `public`. |
| `healthPath` | none | A path that answers 2xx when an instance can take requests: Knative's readiness probe, and a liveness probe that restarts a stuck instance. |

The container options below apply to scheduled and queue functions too:

| Option | Default | |
| --- | --- | --- |
| `memoryLimit` | `memory` | Memory the container may use above its request, for spikes: `{ memory: "384Mi", memoryLimit: "1536Mi" }`. |
| `user` | the image's | Run as this user id and group, for an image whose Dockerfile has no non-root `USER`. |
| `readOnlyRootFilesystem` | `false` | Write only to `volumes`. |
| `volumes` | none | Scratch directories, empty at each start: `[{ path: "/scratch", sizeLimit: "1Gi" }, { path: "/dev/shm", sizeLimit: "512Mi", memory: true }]`. `memory: true` keeps one in RAM (it counts against the memory limit). Knative doesn't allow `/tmp`: point `TMPDIR` at a volume. |

### Node.js request listeners

A framework that's a Node.js request listener (Express, h3's
`toNodeListener`, Nitro) is served as it is with `{ node: listener }`:

```ts
import express from "express";
const app = express();
export const api = onRequest({ public: true }, { node: app });
```

It gets the server's own request and response, so responses stream and
headers arrive as the client sent them. `api.fetch(request)` still works
in tests (through a local server).

## Scheduled functions

`onSchedule("0 3 * * *", handler)`, or `onSchedule(options, handler)` with
`schedule`, `timeZone` (default UTC), `retries` (default 0),
`timeoutSeconds` (default 3600, for the whole run), and the container
options above. The handler gets `{ signal, startedAt }`; `signal` aborts
at the timeout or on SIGTERM. A run that throws or times out exits 1, and
Kubernetes retries it `retries` times. Runs don't overlap: a tick that
comes while the last run is still going is skipped.

## Queues

`onQueue(queue, handler)` or `onQueue(options, handler)`. A queue's name is
lowercase letters, digits, dashes and underscores. `enqueue(queue,
payload)` adds a job from anywhere in the app and resolves to its id;
`enqueue(resize, payload)` takes the queue function itself and checks the
payload's type. Payloads are JSON. Jobs live in the app's Postgres
database (`DATABASE_URL`; the app gets one with `db: true` in its
`maxcloud.yaml`), in a table the SDK creates (`maxcloud_jobs`).

| Option | Default | |
| --- | --- | --- |
| `concurrency` | `1` | Jobs one instance works on at once. |
| `maxInstances` | `3` | |
| `maxAttempts` | `5` | Tries before a job is marked `failed`. |
| `backoffSeconds` | `10` | Delay before the first retry, doubled each time. |
| `timeoutSeconds` | `300` | Per job. A job past it is aborted and retried. |
| `memory`, `cpu` | the namespace's defaults | As for HTTP functions. |

- **Retries:** a job that throws or times out is retried, and so is one
  whose worker died, once its lease (the timeout plus 30 seconds) runs
  out. A job can run more than once, so make handlers safe to repeat.
- **Return when `signal` aborts.** The handler gets `(job, { signal })`;
  `signal` aborts at the timeout or when the worker shuts down. A handler
  that ignores it keeps running after its job has been counted as failed.
- **Shutting down:** on SIGTERM the worker takes no new jobs, gives the
  ones in hand 25 seconds, then puts them back without counting the
  attempt.
- **Enqueue in your own transaction:** pass `{ client }`, and the job exists
  only if your transaction commits:
  `await enqueue("mail", msg, { client: tx })`. The table must exist by
  then (a worker or an earlier `enqueue` creates it).
- **Delay a job** with `{ delaySeconds }`.
- **Cleanup:** a worker deletes jobs that finished more than 7 days ago
  when it starts. Failed jobs stay, with the error in `last_error`.

## Database

With `db: true` in the app's `maxcloud.yaml`, every function gets
`DATABASE_URL`. `db()` is a `pg.Pool` on it, made on first use and shared
with `enqueue` and `kv`, so the app doesn't need a second pool:

```ts
import { db } from "@max-cloud/functions";

const { rows } = await db().query("SELECT id, name FROM users WHERE id = $1", [id]);
```

It throws if `DATABASE_URL` isn't set.

### Migrations

Put SQL migrations in `migrations/` at the app's root (next to
`package.json`), named `NUMBER_DESCRIPTION.sql` with numbers of one width:
`0001_init.sql`, `0002_add_orders.sql`. Before `maxcloud-functions` runs a
function, it applies the ones the database hasn't had: each once, in name
order, in its own transaction, recorded in the table
`maxcloud_migrations` (the one the connection's `search_path` finds, else a
new one in its current schema: `public` on maxcloud). Only one process
applies them at a time; the others wait, so no function starts on an older
schema than its code expects. A migration that fails, a file edited after it
was applied, or migrations without a `DATABASE_URL` stop the process (exit
1). A migration the database has but the folder doesn't is left alone.
`MAXCLOUD_MIGRATE=0` in the environment turns it off.

A `migrations/` folder made by drizzle-kit (it has `meta/_journal.json`)
is skipped: drizzle's migrator applies it, and records it, itself.

It reads `./migrations` from the working directory. Buildpacks keep the
whole app in the image; with your own Dockerfile, copy `migrations/` in
next to `package.json`. `--describe` lists the files, and the deploy warns
when the image is missing any.

Dev mode leaves them to `mcloud dev`, which applies them before it starts
the app; `mcloud db migrate` applies them by hand ([the
rules](https://github.com/maxemiliang/maxcloud/blob/main/docs/databases.md#migrations)). For a script or a test:

```ts
import { migrate } from "@max-cloud/functions";

await migrate({ url: process.env.TEST_DATABASE_URL }); // ./migrations; resolves to the names applied
```

`migrate({ url, dir, log })` defaults to `DATABASE_URL` and
`./migrations`. For tooling there are also `loadMigrations(dir)`,
`migrationStatus(client, files)` (applied, pending, changed or missing,
without changing anything) and `applyMigrations(client, files)`.

## Buckets

An app's S3 buckets are listed in its `maxcloud.yaml`:
`buckets: [uploads, assets:public]`. The maxcloud controller creates them
in UpCloud Object Storage as `mc-<app>--<bucket>`, and every function gets
the app's credentials as the `AWS_*` environment variables any S3 client
reads. `MAXCLOUD_APP`, the app's name, is set on every function. So the AWS
SDK needs no configuration, only the bucket's full name, which
`bucketName` gives. Each bucket's full name is also in
`MAXCLOUD_BUCKET_<NAME>` (`MAXCLOUD_BUCKET_UPLOADS`, `MAXCLOUD_BUCKET_USER_FILES`
for `user-files`), for libraries configured by environment; a pod started
before the bucket existed doesn't have it.

```ts
import { S3Client, PutObjectCommand } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { bucketName } from "@max-cloud/functions";

const s3 = new S3Client({ forcePathStyle: true });
const uploads = bucketName("uploads"); // "mc-shop--uploads"

// A URL the browser uploads to directly; then enqueue the work.
const url = await getSignedUrl(s3, new PutObjectCommand({ Bucket: uploads, Key: "a.png" }), { expiresIn: 300 });
```

Object Storage has no bucket events: uploads tell the app themselves
(presigned URL, then a queue job).

### Public buckets

A bucket listed as `assets:public` can be read by anyone (not listed) at
`https://<bucket>--<app>.gronblom.dev/<key>`, behind Cloudflare, which
caches it. Writing still needs the app's credentials. So a function
uploads to it with the AWS SDK and hands out the public URL:

```ts
const key = `avatars/${userId}.${hash}.png`; // new content, new key: the CDN caches
await s3.send(new PutObjectCommand({
  Bucket: bucketName("assets"),
  Key: key,
  Body: png,
  ContentType: "image/png",
  CacheControl: "public, max-age=31536000, immutable",
}));
const url = `https://assets--${process.env.MAXCLOUD_APP}.gronblom.dev/${key}`;
```

The objects are also readable straight from Object Storage, at
`<AWS_ENDPOINT_URL_S3>/<full bucket name>/<key>` (not cached). Removing
`:public` makes the bucket private again; its objects stay.

## Key-value storage

`kv` is a key-value store (like Workers KV) in the app's Postgres
database (`DATABASE_URL`, so the app needs `db: true` in its
`maxcloud.yaml`). Values are JSON; reads see writes right away. Keys are
1 to 512 characters.

```ts
import { kv } from "@max-cloud/functions";

await kv.set("session:42", { user: "ann" }, { ttlSeconds: 3600 });
const s = await kv.get<{ user: string }>("session:42"); // undefined if missing or expired
await kv.delete("session:42");
const { keys, cursor } = await kv.list({ prefix: "session:", limit: 100 });
```

`delete` resolves to whether the key was there. `list` gives keys in
order, `limit` at a time (default 100, also for 0; at most 1000), with a `cursor` to
pass back while there are more. `set` with `undefined` throws; use
`delete`.

It's one table, `maxcloud_kv`, made on first use. Pass `{ client }` to
`set` or `delete` to write in your own transaction (the table must exist
by then).

## Secrets

`mcloud secrets set STRIPE_KEY` (in the app's repo) stores a secret; every
function gets it as an environment variable. `secret("STRIPE_KEY")` reads
it, and throws an error that says how to set it if it isn't set (or is
empty). Read secrets in handlers,
not at module load: `--describe` loads the module without them.

## Running

The container runs `maxcloud-functions`, which loads package.json's
`main` (or `--source FILE`, or `$FUNCTION_SOURCE`) and starts the function
named in `FUNCTION_TARGET` (or `--target`; the export's name works too):

- **HTTP:** a server on `$PORT` (or `--port`, else 8080).
- **Schedule:** one run, then it exits.
- **Queue:** the worker loop.

First it applies the app's pending migrations (see Migrations). On SIGTERM
an HTTP server or worker finishes what's in flight (up to 25 seconds) and
exits 0, so pods don't end in `Error`; a scheduled run is aborted. Your
app's `package.json` needs:

```json
{
  "type": "module",
  "main": "dist/index.js",
  "scripts": { "start": "maxcloud-functions" }
}
```

`maxcloud-functions --describe` prints the app's functions, their
settings and the migrations it found as JSON. With `MAXCLOUD_DESCRIBE=1`
in the environment it prints the same as one line,
`maxcloud:describe:{...}`. That's how the deploy asks the built image: it
runs it with that variable and its own start command, and reads the line.
`mcloud generate` runs `npx maxcloud-functions --describe` in the app's
folder instead. Either way `MAXCLOUD_DESCRIBE=1` is set while the module
loads, without the database or secrets, so module-level code can skip
connecting to things.

`onShutdown(fn)` runs `fn` after SIGTERM, once the requests or jobs in
flight are done and before the process exits: close a pool, flush logs.
Hooks run in order, for at most 10 seconds together; if one throws, or
they're still running after 10 seconds, the process exits 1.

A bundled server can be its own entrypoint instead of package.json's
`main` (`@max-cloud/nuxt` builds Nuxt this way): `start(exports)` does what
`maxcloud-functions` does for those exports, and `isEntrypoint` tells
whether the module is the one `node` was started with:

```ts
export const web = onRequest({ public: true }, { node: listener });
if (await isEntrypoint(import.meta.url)) start({ web });
```

## Local development

Run `mcloud dev` in the app's repo. It starts a local Postgres (in Docker)
or port-forwards to a database branch, sets `DATABASE_URL`, `MAXCLOUD_APP`
and the secrets from `.env` (keep it out of git), and runs the app in dev
mode under `node --watch`, so edits reload.

Dev mode is `maxcloud-functions dev [--entry FILE] [--port N]`. It runs the
whole app in one process, with `MAXCLOUD_DEV=1` set:

- One server on 127.0.0.1, on `--port`, `$PORT` or 8080.
- `http://<name>.localhost:<port>/` reaches the HTTP function deployed as
  `<name>`. The request goes through as it is (path, method, headers,
  body), so a framework mounted at `/` works.
- `GET http://localhost:<port>/` lists the functions as JSON.
- `POST http://localhost:<port>/_run/<name>` runs a scheduled function once.
  It answers 200 `ok`, or 500 with the error. Schedules never run on their
  cron in dev mode.
- Queue workers run in the same process when `DATABASE_URL` is set. Without
  it they don't, and `enqueue` fails as it would anywhere else.

At startup it prints each function and where to reach it. Ctrl-C finishes
the requests and jobs in flight, then exits.

`FILE` defaults to `src/index.ts` if there is one, else `package.json`'s
`main`. Node runs a `.ts` file by stripping its types (Node 22.18 or newer;
no build step), so relative imports in it need their `.ts` extensions:
`import { app } from "./app.ts"`. Set `allowImportingTsExtensions` (or
`rewriteRelativeImportExtensions`) in `tsconfig.json` to make `tsc` accept
them. Type stripping also can't run TypeScript-only syntax such as `enum`.

To run a single function the way its container does (it loads `main`, so
build first):

```fish
FUNCTION_TARGET=api npx maxcloud-functions
```

## Testing

`@max-cloud/functions/testing` has two helpers for tests (any runner:
`node:test`, Vitest, ...). It's a separate entry point, so it stays out of
the app's production bundle.

### A throwaway database

`testDatabase()` creates a uniquely named schema (`maxcloud_test_...`) in
the Postgres at `TEST_DATABASE_URL` and points the SDK at it until
`drop()`: `db()`, `kv` and `enqueue` use it, and `DATABASE_URL` is set to a
URL whose connections start with `search_path` set to the schema, so queue
workers and child processes land there too. `drop()` closes its pool,
drops the schema with everything in it and puts back the previous
`DATABASE_URL` and `db()` pool.

```ts
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { db, kv } from "@max-cloud/functions";
import { testDatabase, type TestDatabase } from "@max-cloud/functions/testing";

const skip = process.env.TEST_DATABASE_URL ? false : "set TEST_DATABASE_URL";
let t: TestDatabase;
before(async () => { if (!skip) t = await testDatabase(); });
after(() => t?.drop());

test("signup", { skip }, async () => {
  await db().query("CREATE TABLE users (email text PRIMARY KEY)"); // or: await migrate()
  await kv.set("welcome", true);
  assert.equal(await kv.get("welcome"), true);
});
```

Any Postgres you can create schemas in will do:

```fish
docker run -d --rm --name pg-test -p 127.0.0.1:5433:5432 -e POSTGRES_PASSWORD=test postgres:18
set -x TEST_DATABASE_URL postgres://postgres:test@127.0.0.1:5433/postgres
```

Only the schema is on the search path, not `public`: a table your test
didn't create there doesn't exist, and an extension installed in `public`
is only reachable as `public.<name>`. `t.url`, `t.pool` and `t.schema` are
there for your own connections. `migrate()` applies the app's migrations
into the schema (it uses `DATABASE_URL`), and records them in the schema's
own `maxcloud_migrations`, so every test database gets them all.
`testDatabase({ use: false })` makes the
schema without touching `db()` or `DATABASE_URL`. A schema left behind by a
crashed run can be dropped by hand; they all start with `maxcloud_test_`.

### In-memory buckets

`testBuckets()` starts an S3 server in the test process, on
`127.0.0.1` with a random port, and sets the `AWS_*` variables a function
gets on maxcloud to point at it (and `MAXCLOUD_APP` to `test` if it isn't
set). The app's own S3 client then talks to it unchanged: the AWS SDK,
aws4fetch, unstorage's `s3` driver. Objects live in memory; signatures
aren't checked.

```ts
import { bucketName } from "@max-cloud/functions";
import { testBuckets } from "@max-cloud/functions/testing";

const s3 = await testBuckets({ buckets: ["uploads"] }); // bucketName("uploads") exists
s3.put(bucketName("uploads"), "in/cat.png", png, "image/png"); // a starting point
await resizeImage({ key: "in/cat.png" });                       // your code, using the AWS SDK
assert.ok(s3.get(bucketName("uploads"), "out/cat.png"));        // a Buffer, or undefined
await s3.close();                                               // puts the environment back
```

It answers PutObject, GetObject (with `Range`), HeadObject, DeleteObject,
DeleteObjects, CopyObject, ListObjectsV2 and ListObjects, and creating,
listing and deleting buckets, with S3's XML and error codes (`NoSuchKey`,
`NoSuchBucket`). Multipart uploads (`@aws-sdk/lib-storage`'s `Upload` for
bodies over 5 MB) answer 501. Requests are path-style
(`http://127.0.0.1:PORT/<bucket>/<key>`); the AWS SDK uses that for an IP
endpoint by itself. The helper's own methods take a bucket's full name:
`createBucket`, `get`, `object` (body and headers), `put`, `keys(bucket,
prefix)`, `clear()` (empties every bucket); `s3.env` is the `AWS_*`
variables, for a child process. `testBuckets({ use: false })` leaves the
environment alone.

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
`resize-image`.

## HTTP functions

`onRequest(handler)` or `onRequest(options, handler)`. The handler takes a
standard `Request` and returns a `Response`, so fetch-style frameworks work
as they are: `onRequest(app.fetch)` with Hono, for example.

| Option | Default | |
| --- | --- | --- |
| `public` | `false` | Reachable from the internet at `https://<name>-<app>.gronblom.dev`. Otherwise cluster-local. |
| `minInstances` | `0` | Instances kept running when idle. |
| `maxInstances` | `3` | |
| `concurrency` | `0` | Requests one instance handles before another starts (0: no limit). |
| `timeoutSeconds` | `300` | Per request. |

## Scheduled functions

`onSchedule("0 3 * * *", handler)`, or `onSchedule(options, handler)` with
`schedule`, `timeZone` (default UTC), `retries` (default 0) and
`timeoutSeconds` (default 3600). A run that throws exits 1, and Kubernetes
retries it `retries` times.

## Queues

`onQueue(queue, handler)` or `onQueue(options, handler)`. `enqueue(queue,
payload)` adds a job from anywhere in the app. Jobs live in the app's
Postgres database (`DATABASE_URL`), in a table the SDK creates
(`maxcloud_jobs`).

| Option | Default | |
| --- | --- | --- |
| `concurrency` | `1` | Jobs one instance works on at once. |
| `maxInstances` | `3` | |
| `maxAttempts` | `5` | Tries before a job is marked `failed`. |
| `backoffSeconds` | `10` | Delay before the first retry, doubled each time. |
| `timeoutSeconds` | `300` | Per job. A job past it is aborted and retried. |

- **Retries:** a job that throws is retried, and so is one whose worker
  died, once its lease runs out. A job can run more than once, so make
  handlers safe to repeat.
- **Enqueue in your own transaction:** pass `{ client }`, and the job exists
  only if your transaction commits:
  `await enqueue("mail", msg, { client: tx })`.
- **Delay a job** with `{ delaySeconds }`.

## Running

The container runs `maxcloud-functions`, which starts the function named in
`FUNCTION_TARGET`:

- **HTTP:** a server on `$PORT`.
- **Schedule:** one run, then it exits.
- **Queue:** the worker loop.

On SIGTERM it finishes what's in flight and exits 0, so pods don't end in
`Error`. Your app's `package.json` needs:

```json
{
  "type": "module",
  "main": "dist/index.js",
  "scripts": { "start": "maxcloud-functions" }
}
```

To try a function locally:

```fish
FUNCTION_TARGET=api npx maxcloud-functions
```

`maxcloud-functions --describe` prints the app's functions and their
settings as JSON; `mcloud` deploys from that. While it runs,
`MAXCLOUD_DESCRIBE=1` is set, so module-level code can skip connecting to
things.

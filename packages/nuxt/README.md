# @max-cloud/nuxt

A Nuxt 4 app on maxcloud, the homelab's run platform
([maxemiliang/maxcloud](https://github.com/maxemiliang/maxcloud)):

- **runtimeConfig from the platform.** maxcloud gives an app
  `DATABASE_URL`, its Object Storage credentials (`AWS_*`), its buckets'
  names and `MAXCLOUD_APP` as environment variables; Nuxt only reads
  `NUXT_*` ones. The module fills them in, so there's nothing to map per
  variable in the app's manifests.
- **The app as a maxcloud function.** With `function`, `nuxt build` makes
  the server an HTTP function (and Nitro's scheduled tasks scheduled
  functions), so the deploy workflow writes its Knative service with
  `functions: true`, like any functions app.

```fish
npm install @max-cloud/nuxt @max-cloud/functions
```

```ts
// nuxt.config.ts
export default defineNuxtConfig({
  modules: ["@max-cloud/nuxt"],
  runtimeConfig: {
    databaseUrl: "",
    storage: { s3: { endpoint: "", region: "", bucket: "", accessKeyId: "", secretAccessKey: "" } },
  },
  maxcloud: {
    // The app's own keys: where each value comes from on maxcloud
    runtimeConfig: { databaseUrl: "database", "storage.s3": "s3:uploads" },
    // The server as the function "web" (onRequest's options)
    function: { name: "web", public: true, minInstances: 1 },
  },
});
```

## runtimeConfig

`useRuntimeConfig().maxcloud` is always there (server side):

| Key | From |
| --- | --- |
| `app` | `MAXCLOUD_APP` (at build time: `maxcloud.yaml`'s `app`) |
| `databaseUrl` | `DATABASE_URL` |
| `s3.endpoint`, `s3.region`, `s3.accessKeyId`, `s3.secretAccessKey` | `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| `buckets.<name>` | the bucket's full name, `mc-<app>--<name>`, for each bucket in `maxcloud.yaml` |

`maxcloud.runtimeConfig` fills the app's own keys (dotted paths) from:

| Source | Value |
| --- | --- |
| `"app"` | the app's name |
| `"database"` | the database's URL |
| `"s3.endpoint"`, `"s3.region"`, `"s3.accessKeyId"`, `"s3.secretAccessKey"` | the credentials |
| `"bucket:<name>"` | a bucket's full name |
| `"s3:<bucket>"` | on an object key: `endpoint`, `region`, `accessKeyId`, `secretAccessKey` and `bucket` under it |
| `"env:<NAME>"` | any environment variable |

A `NUXT_*` variable you set yourself wins. Without Object Storage
credentials (`mcloud dev` without `--buckets`) bucket names stay empty, so
the app can tell S3 isn't there.

## As a function

`function: { name, ...onRequest options }` (`function: true` is
`{ name: "web" }`). The options are `@max-cloud/functions`' `onRequest`
ones: `public`, `proxied`, `minInstances`, `maxInstances`, `concurrency`,
`timeoutSeconds`, `memory`, `memoryLimit`, `cpu`, `user`,
`readOnlyRootFilesystem`, `volumes`, `healthPath`.

`nuxt build` then makes `.output/server/index.mjs` a functions module:

- `web` (the name): the Nitro app as a Node.js request listener. Responses
  stream, the app sees the request's own `Host`, static files and
  `/_nuxt/` are served as by Nitro's own server.
- `task-<name>` for each task in `nitro.scheduledTasks`: a scheduled
  function (a CronJob) that runs the task once, with the web function's
  container settings (`user`, `volumes`, memory). Nitro's own task runner
  doesn't run: a function scales to zero. `tasks: false` leaves them out.
- Nitro plugins run when the server starts, before it takes requests
  (database migrations, storage mounts), but not when mcloud only asks the
  image for its functions (`MAXCLOUD_DESCRIBE=1`).
- On SIGTERM it finishes the requests in flight, then calls Nitro's
  `close` hook, then exits 0.
- `@max-cloud/functions` applies `migrations/*.sql` when a function starts;
  in a Nuxt build that's off (`MAXCLOUD_MIGRATE=0`), since Nuxt apps
  migrate in a Nitro plugin, often from a drizzle-kit `migrations/` folder
  with the same file names. `migrations: true` turns it on.

The image runs it either way:

- **Dockerfile:** `CMD ["node", ".output/server/index.mjs"]`. Run with
  `node`, the module is its own entrypoint: it serves `$FUNCTION_TARGET`
  (default: the web function) on `$PORT`, or describes itself.
- **Buildpacks:** `"main": ".output/server/index.mjs"` and
  `"start": "maxcloud-functions"` in package.json, as for any functions app.

Then deploy with `functions: true` in the app's deploy workflow
(maxcloud's `run-deploy.yaml`): it writes `functions.yaml` (the Knative
service, and a CronJob per task) next to the app's own manifests.

## Local development

`mcloud dev` runs `nuxt dev` for a Nuxt app, with the environment it
gives any app: a local Postgres or a branch (`DATABASE_URL`), `.env`
secrets, `MAXCLOUD_APP`, and the real buckets with `--buckets`. The module
turns that into runtimeConfig in dev too.

## Server helpers

```ts
import { drizzleDb, s3Driver } from "@max-cloud/nuxt/server";

// unstorage on one of the app's buckets
export default defineNitroPlugin(async () => {
  useStorage().mount("files", await s3Driver("uploads"));
});

// Drizzle on the app's database, sharing @max-cloud/functions' pool (db())
const db = await drizzleDb(schema);
```

They need `unstorage` and `drizzle-orm` installed; `bucketName` and `db`
are re-exported from `@max-cloud/functions`.

## How it works, and why

- The `NUXT_*` variables have to be set before Nitro reads its
  runtimeConfig, which it does once, when its chunk loads, and freezes. The
  module puts a small module first in the server entry
  (`#maxcloud/env`). It must not import anything Nitro's chunk also uses
  (scule, h3): Rollup puts those in the shared chunk, and importing it
  would create the app first. The `NUXT_*` names are worked out at build
  time instead. Without `function`, the module wraps Nitro's own
  `node-server` entry the same way.
- The function entry imports Nitro's runtime and h3 dynamically, for the
  same reason, and so describing the image doesn't run plugins.
- It's a Node listener, not h3's `toWebHandler`: in h3 1.x that runs the
  whole request against a fake response and returns the body when it's
  done, so nothing streams.

## Development

`test/fixture` is a small app shaped like lankkupaja (a startup plugin
with a close hook, runtimeConfig in its own keys, an SSR page, a streaming
route, a scheduled task). `npm test` builds it and runs it as maxcloud
would; `MAXCLOUD_SKIP_NUXT_BUILD=1` reuses the last build. Build
`@max-cloud/functions` first (`npm run build --workspaces` at the root).

It depends on `@max-cloud/functions` `>=0.5.0 <1.0.0`, not `^0.5.0`: below
1.0.0 a caret allows no new minor version, so the next SDK release would
stop npm from linking the workspace's copy.

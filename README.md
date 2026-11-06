# maxcloud

Deploy apps and functions to my homelab, the way you would to Cloud Run or
Firebase. maxcloud is Knative on the homelab cluster, deployed through Flux
from [maxemiliang/homelab](https://github.com/maxemiliang/homelab).

This repo holds:

- `mcloud`, the command line (Go, `cmd/mcloud`).
- `@max-cloud/functions`, the Node.js/TypeScript SDK for writing HTTP,
  scheduled and queue functions (`packages/functions`, see its README).
- `github.com/maxemiliang/maxcloud/functions`, the same for Go (`functions/`,
  see its README).
- `maxcloud-controller` (`controller/`), which gives apps their S3
  buckets and Postgres databases, keeps their secrets safe through a
  cluster rebuild, and describes the platform to mcloud. See its README.

## Getting started

```fish
mise use -g github:maxemiliang/maxcloud   # or: go install github.com/maxemiliang/maxcloud/cmd/mcloud@latest
mcloud login                              # once per machine: log in, and set up cluster access
mcloud doctor                             # if anything's off: says what, and how to fix it

mkdir shop && cd shop && git init
mcloud init                               # a starter functions app (--lang go for Go)
npm install
mcloud dev                                # run it here: http://hello.localhost:8080/
gh repo create --source . --push --private
mcloud deploy                             # register it, push, follow the build and the rollout
```

## How an app gets deployed

1. **Set up the repo.** `mcloud init` writes a starter functions app (or
   adds what an existing Node.js or Go app needs), its deploy workflow and
   a `maxcloud.yaml`: the whole app's definition, like a `wrangler.toml`.
2. **Register the app.** The first `mcloud deploy` does it, from
   `maxcloud.yaml`. It adds the app's entry to the registry in the cluster,
   with your login: no commit to the homelab repo. From that entry the
   platform creates the app's namespace with its quota and guardrails, its
   buckets and database, and a reconciler for the app's `deploy/k8s`. After
   that, a pushed change to `maxcloud.yaml` reaches the entry by itself (the
   controller reads it from Flux), within the limits a developer may ask
   for; `mcloud deploy` does it too. The maxcloud controller keeps a copy of
   every entry in Object Storage and puts them back after a cluster
   rebuild.
3. **Push to deploy.** Every push to `main` runs the deploy workflow
   (`.github/workflows/run-deploy.yaml` in this repo, which app repos call).
   It's `mcloud build --ci`:
   - it builds the image (the repo's Dockerfile, else Paketo buildpacks)
     and pushes it to `registry.gronblom.dev/<app>/<app>` as the repo
     itself (GitHub's OIDC token; no secrets);
   - it asks the image which functions it has and writes their manifests
     to `deploy/k8s`;
   - it pins the image digest and commits.

   Flux then deploys that commit. `mcloud deploy` pushes and follows all
   of it until the services are ready. If Flux can't read a new repo (the
   homelab's Flux GitHub App doesn't have it yet), it says so and opens the
   page where you add the repo to the app, then carries on once Flux can.

Each kind of function becomes:

| Function | Runs as |
| --- | --- |
| `onRequest` | a Knative service: scales to zero, private unless `public: true` |
| `onSchedule` | a CronJob |
| `onQueue` | a Deployment that KEDA scales from 0 on the queue's backlog |

Queues keep their jobs in the app's Postgres database: `db: true` in
`maxcloud.yaml` (mcloud says so when an app with a queue has none).

The homelab README's "Run an app" covers the platform side, and its
`templates/run-app` is the starting point for apps that aren't functions;
they use the same workflow without `functions: true`.

## maxcloud.yaml

```yaml
app: shop
quota: {cpu: 500m, memory: 2Gi}
buckets: [uploads, assets:public]
db: {size: 2Gi}          # or db: true
# source: worker         # build the app from this folder, not the repo's root
# deploy: direct         # mcloud deploy builds and applies from here, no git
```

It owns what it declares: a push (or `mcloud deploy`) applies it, and
`mcloud apps set` refuses to change those fields, since the next push would
undo them. What it leaves out stays as it is. Changing `deploy:` switches
the app between GitOps and direct on the next `mcloud deploy`, which asks
first.

`source:` is the folder the app lives in, relative to `maxcloud.yaml`
(default: the folder `maxcloud.yaml` is in). Use it when the repo's root is
something else, e.g. a Go CLI at the root with the web app in `worker/`:
buildpacks would otherwise detect Go. Everything that builds or runs the
app uses it: `mcloud build`, direct deploys (local and `--build cluster`,
which streams only that folder), the deploy workflow (unless its `context`
input says otherwise), `mcloud generate` and `mcloud dev`. A Dockerfile and
`project.toml` are looked for there. It has to be a directory inside the
repo, without `..`. The manifests stay where they are (`deploy/k8s` at the
root).

## Local development

`mcloud dev` runs the whole app on your machine, with what it gets on
maxcloud:

- every HTTP function at `http://<name>.localhost:8080/` (a list at
  `http://localhost:8080/`), the queue workers, and scheduled functions on
  `POST http://localhost:8080/_run/<name>`;
- `DATABASE_URL`: a local Postgres in Docker (kept between runs; the
  default when `maxcloud.yaml` has `db:`), or a branch of the app's
  database through a port-forward (`--db branch:NAME`);
- `MAXCLOUD_APP`, and secrets from `.env` (keep it out of git), or the
  app's own (`--secrets remote`); the app's real buckets only with
  `--buckets`.

TypeScript runs from source and restarts on changes. To poke the deployed
app: `mcloud call api /health` sends a request to any function, private
ones too, and `mcloud run nightly` runs a scheduled function now.

## Direct deploys

For quick functions and experiments there's no need for git at all:

```fish
mcloud init --deploy direct                 # a starter with deploy: direct in maxcloud.yaml
mcloud deploy                               # register, build here, push, apply, wait until ready
```

`mcloud deploy` builds the working tree, uncommitted changes included, with
`mcloud build`, the same build CI runs: with the Dockerfile, or with
buildpacks (`pack`, or pack in a container if only docker is installed).
Without docker, an app without a Dockerfile builds in the cluster instead
(`--build cluster`, chosen by itself): a buildpacks pod in the app's
namespace, with the source streamed to it. The image goes to the maxcloud
registry with a token of the app's push ServiceAccount that lasts an hour
(whoever may manage the app's namespace may push its images); mcloud is
docker's credential helper while it builds, so there's no docker login and
nothing is written to `~/.docker`. Then it asks the image for its
functions and applies their manifests. Functions you removed are deleted;
the database never is.

What you give up: nothing is in git, so a cluster rebuild doesn't bring a
direct app back (run `mcloud deploy` again), and there's no history to
revert to. Use GitOps (the default) for anything that matters: remove
`deploy: direct` from `maxcloud.yaml` and `mcloud deploy`.

`image:` in `maxcloud.yaml` pushes somewhere else instead (log in to it
yourself); the cluster then needs to be able to pull from there.

## Buckets

An app gets S3 buckets (in the homelab's UpCloud Object Storage, the way a
Worker gets R2) from `buckets:` in `maxcloud.yaml`. The
[controller](controller/README.md) turns each into a bucket
`mc-shop--uploads` and puts the app's credentials in the Secret
`maxcloud-s3`, which every function gets as environment variables. The
SDKs' `bucketName("uploads")` gives the full name. Removing a bucket from
the list keeps it and its objects.

### Public buckets

Add `:public` to a bucket's name in the list, and anyone can read its
objects (not list them) at a hostname of its own:

```yaml
buckets: [uploads, assets:public]
```

```
https://assets--shop.gronblom.dev/logo.png      # object logo.png in mc-shop--assets
```

- It's for files you'd put on a CDN: images, downloads, built front-end
  assets. Only GET and HEAD go through; writing still takes the app's
  credentials (from a function, or a presigned URL it hands out).
- Cloudflare serves the hostname and caches what it serves, by file
  extension and the object's `Cache-Control`. A changed object can be
  served stale until that runs out, so give changed files new keys
  (`app.3f2a91.js`), or set `Cache-Control` when you upload.
- The same objects are also readable straight from Object Storage, at
  `<AWS_ENDPOINT_URL_S3>/mc-shop--assets/logo.png` (not cached).
- `kubectl get maxcloud -n shop` shows each bucket's URL.
- Removing `:public` makes the bucket private again: its hostname and the
  public read go, its objects stay.

## Databases

An app gets a Postgres database (the way a Worker gets D1, but Postgres)
from `db:` in `maxcloud.yaml`. The controller runs it with CloudNativePG,
backs it up continuously to the bucket `mc-shop--db`, and restores it from
there by itself if its cluster is lost. Every function gets
`DATABASE_URL`. An app set up before this may have a `database.yaml` (what
mcloud used to write for queue functions, with no backups): it keeps
working, but the controller won't take it over. Moving it is a dump and
restore (homelab `docs/runbooks/move-database-to-registry.md`).

```fish
mcloud db psql                      # psql in it (--branch NAME for a branch)
mcloud db proxy                     # on localhost, with a DATABASE_URL to use
mcloud db restore --at 15m          # as it was 15 minutes ago, in place
mcloud db branch create try         # a throwaway copy (--at for a point in time)
```

`restore` and `branch` restore from the backups, so they reach back as far
as the retention (7 days by default).

## Secrets and key-value storage

`mcloud secrets set NAME` stores an app's secret, like `wrangler secret
put`. Every app keeps them in the Secret `app-secrets` in its namespace:
nothing to commit and no keys to hold, and the app's HTTP and queue
functions restart so they have it right away. Every function gets the
secrets as environment variables; the SDKs' `secret("NAME")` reads one. The
maxcloud controller keeps an encrypted copy and puts it back after a
cluster rebuild. A GitOps app that still has
`deploy/k8s/secrets.sops.yaml` (how secrets were kept before) moves off it
with `mcloud secrets migrate`, which takes over the Secret Flux already
decrypted.

For KV, the SDKs' `kv` stores JSON values in the app's database.

## Logging in

mcloud (and kubectl) log in to the cluster through the homelab's Authelia,
and act with what your groups allow. `mcloud login` is all the setup:

- it reads the platform's discovery document
  (`https://maxcloud.gronblom.dev/.well-known/maxcloud.json`,
  `$MCLOUD_PLATFORM_URL`): the cluster's address and CA, and the oldest
  mcloud that works with it (older ones warn);
- it logs in: a device code you approve in any browser (`--browser`: this
  machine's);
- it writes mcloud's kubeconfig, `~/.config/mcloud/kubeconfig`, which
  mcloud uses from then on. For kubectl: `set -gx KUBECONFIG
  ~/.config/mcloud/kubeconfig`.

The kubeconfig runs `mcloud credential` for a token: the ID token lasts an
hour and is refreshed by itself; logging in again is needed after 12 hours
or `mcloud logout`. mcloud picks its kubeconfig from `--kubeconfig`, else
`$MCLOUD_KUBECONFIG`, else its own, else `$KUBECONFIG`. If Authelia or the
cluster is down, get an admin kubeconfig with `upctl kubernetes config`
(the homelab's break-glass) and pass it with `--kubeconfig`.

GitOps deploys follow the build on GitHub with `$GH_TOKEN`, else your
GitHub CLI login (`gh auth login`).

## Commands

```fish
mcloud login                        # set up and log in (mcloud doctor checks it)
mcloud init                         # in a new repo: a functions app (--lang go for Go)
mcloud dev                          # run it here
mcloud deploy                       # register, push, follow build, Flux and rollout

mcloud status                       # deploy, services and URLs, workloads, databases, problems
mcloud logs -f                      # logs of every pod; mcloud logs shop web -f for one service
mcloud call api /health             # a request to a function, private ones too
mcloud run nightly                  # a scheduled function, now

mcloud secrets set STRIPE_KEY       # asks for the value; restarts the functions
mcloud secrets list                 # names only
mcloud secrets unset STRIPE_KEY

mcloud db psql                      # and db proxy, restore, recover, branch
mcloud queue ls                     # jobs per queue and state, latest failed ones
mcloud queue retry 41 42            # run failed jobs again (or --failed)

mcloud apps list                    # registered apps, did the last deploy apply
mcloud apps set shop --deploy direct  # by hand: what maxcloud.yaml leaves out
mcloud apps delete shop             # remove it, namespace and data included

mcloud build                        # build and push the image (what deploys run)
mcloud generate                     # what deploy/k8s would become (asks the built app)
```

On a terminal mcloud shows its progress with spinners and colors and asks
with interactive prompts (`mcloud init` asks for the name, language and
deploy mode). In pipes, in CI, with `NO_COLOR` or `MCLOUD_PLAIN=1`, or with
`--plain`, it prints plain lines instead.

Run inside an app's repo, commands take the app from its `maxcloud.yaml`
(or `$MCLOUD_APP`). `apps create`, `set` and `delete` change the registry
in the cluster (no commit anywhere), and show the change and ask first.
Pass `--dry-run` to only see it, or `--yes` to skip the question. `delete`
asks you to type the app's name. Apps registered in the homelab repo itself
(the platform's own, like run-demo) change there instead.

## Releasing

Each part has its own tags; pushing one releases it.

| Tag | Releases | How |
| --- | --- | --- |
| `v0.2.0` | `mcloud` binaries on GitHub Releases | `release.yaml` (GoReleaser) |
| `controller/v0.2.0` | the controller image | `release-controller.yaml` (see `controller/README.md`) |
| `functions-js/v0.2.0` | `@max-cloud/functions` on npm, after bumping `packages/functions/package.json` | `release-npm.yaml` (trusted publishing, no token) |
| `functions/v0.2.0` | the Go SDK | the tag is the release (Go modules) |

`run-deploy.yaml` installs the newest `mcloud` release (`v*`) when it runs,
so a workflow change that needs a new mcloud goes out after that release.
`mcloud init` writes the newest `@max-cloud/functions` from npm into a new
app; bump `sdkFallback` in `internal/cli/sdkversion.go` with each SDK
release for when npm can't be asked.

## Development

```fish
go test ./...
go run ./cmd/mcloud --help

npm ci
# The queue tests need a Postgres; without TEST_DATABASE_URL they're skipped.
docker run -d --rm --name pg -p 127.0.0.1:55432:5432 -e POSTGRES_PASSWORD=test postgres:18
set -x TEST_DATABASE_URL postgres://postgres:test@127.0.0.1:55432/postgres
npm test --workspaces
```

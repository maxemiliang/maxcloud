# maxcloud

Deploy apps and functions to my homelab, the way you would to Cloud Run or
Firebase. maxcloud is Knative on the homelab cluster, deployed through Flux
from [maxemiliang/homelab](https://github.com/maxemiliang/homelab).

This repo holds:

- `mcloud`, the command line (Go, `cmd/mcloud`). Available now.
- `@max-cloud/functions`, the Node.js/TypeScript SDK for writing HTTP,
  scheduled and queue functions (`packages/functions`, see its README).
- `github.com/maxemiliang/maxcloud/functions`, the same for Go (`functions/`,
  see its README).
- `maxcloud-controller` (`controller/`), which gives apps their S3
  buckets and Postgres databases. See its README.

## How an app gets deployed

1. **Set up the repo.** `mcloud init` writes a starter functions app (or
   adds what an existing Node.js app needs), its deploy workflow and a
   `maxcloud.yaml`.
2. **Register the app.** `mcloud apps create` (or `init --register`)
   commits one entry to the homelab repo's app registry
   (`apps/maxcloud/apps/<name>.yaml`). From that entry, Flux creates the
   app's namespace, with a quota and guardrails, and a reconciler for the
   app's `deploy/k8s`.
3. **Push to deploy.** Every push to `main` runs the deploy workflow
   (`.github/workflows/run-deploy.yaml` in this repo, which app repos call):
   - it builds the image (buildpacks, or the repo's Dockerfile);
   - it asks the image which functions it has, and `mcloud generate`
     writes their manifests to `deploy/k8s`;
   - it pins the image digest and commits.

   Flux then deploys that commit. `mcloud deploy` pushes and follows all
   of it until the services are ready.

Each kind of function becomes:

| Function | Runs as |
| --- | --- |
| `onRequest` | a Knative service: scales to zero, private unless `public: true` |
| `onSchedule` | a CronJob |
| `onQueue` | a Deployment that KEDA scales from 0 on the queue's backlog |

Apps with a queue also get a Postgres database (`database.yaml`, written
once and then yours to change).

The homelab README's "Run an app" covers the platform side, and its
`templates/run-app` is the starting point for apps that aren't functions;
they use the same workflow without `functions: true`.

## Direct deploys

For quick functions and experiments there's no need for git at all:

```fish
mcloud init --deploy direct                 # a starter with deploy: direct in maxcloud.yaml
mcloud apps create quick --deploy direct    # namespace and quota, nothing from git
mcloud deploy                               # build here, push, apply, wait until ready
```

`mcloud deploy` builds the working tree, uncommitted changes included: with
the Dockerfile, or with `pack` (buildpacks) if there's none. It pushes the
image to GHCR (`docker login ghcr.io` with a token that has
`write:packages`), asks it for its functions, and applies their manifests
with your kubeconfig. Functions you removed are deleted; the database never
is.

What you give up: nothing is in git, so a cluster rebuild doesn't bring a
direct app back (run `mcloud deploy` again), and there's no history to
revert to. Use GitOps (the default) for anything that matters. `mcloud apps
set <app> --deploy gitops` switches an app over.

A new GHCR package pushed from your machine starts out private, and the
cluster pulls without credentials: make it public on GitHub (the package's
settings) after the first push.

## Buckets

An app gets S3 buckets (in the homelab's UpCloud Object Storage, the way a
Worker gets R2) from its registry entry:

```fish
mcloud apps set shop --buckets uploads,assets:public
```

[`controller/`](controller/README.md) turns each into a bucket
`mc-shop--uploads` and puts the app's credentials in the Secret
`maxcloud-s3`, which every function gets as environment variables. The
SDKs' `bucketName("uploads")` gives the full name. Removing a bucket from
the list keeps it and its objects.

## Databases

An app gets a Postgres database (the way a Worker gets D1, but Postgres)
from its registry entry too:

```fish
mcloud apps set shop --db                  # --db-size 2Gi, --db-instances 2
```

The controller runs it with CloudNativePG, backs it up continuously to the
bucket `mc-shop--db`, and restores it from there by itself if its cluster
is lost. Every function gets `DATABASE_URL`. An app with a `database.yaml`
(what mcloud writes for queue functions) already has a database `db`; the
controller won't take that one over, so drop the file before switching.

`mcloud db restore --at TIME` takes it back to a point in time (in place;
later writes are gone from it), and `mcloud db branch create NAME [--at
TIME]` makes a throwaway copy with its own `DATABASE_URL`-style URL, for
trying a migration or a preview. Both restore from the backups, so they
reach back as far as the retention (7 days by default).

## Secrets and key-value storage

`mcloud secrets set NAME` stores an app's secret, like `wrangler secret
put`. A GitOps app keeps it in `deploy/k8s/secrets.sops.yaml`, encrypted
with SOPS to the homelab's age key (Flux decrypts it): commit and push. A
direct app keeps it in a Secret on the cluster. Every function gets the
app's secrets as environment variables; the SDKs' `secret("NAME")` reads
one. For KV, the SDKs' `kv` stores JSON values in the app's database.

## Install

```fish
mise use -g github:maxemiliang/maxcloud   # a release binary
# or
go install github.com/maxemiliang/maxcloud/cmd/mcloud@latest
```

`mcloud` reads the cluster with the kubeconfig in `$MCLOUD_KUBECONFIG`, else
the usual `$KUBECONFIG`. It writes to GitHub with `$GH_TOKEN`, else your
GitHub CLI login (`gh auth login`). In fish:

```fish
set -Ux MCLOUD_KUBECONFIG ~/.kube/homelab.yaml
```

## Commands

```fish
mcloud init                                         # in a new repo: a functions app (--lang go for Go)
mcloud apps create shop --repo maxemiliang/shop     # register it (once)
mcloud deploy                                       # push, follow build, Flux and rollout

mcloud apps list                                    # registered apps, did the last deploy apply
mcloud apps set shop --cpu 500m --memory 2Gi        # change its quota
mcloud apps set shop --buckets uploads,assets:public  # its S3 buckets
mcloud apps set shop --db                             # a Postgres database, backed up

mcloud queue ls shop                # jobs per queue and state, latest failed ones
mcloud queue retry shop 41 42       # run failed jobs again (or --failed)

mcloud db restore shop --at 15m     # the database as it was 15 minutes ago, in place
mcloud db branch create shop try    # a throwaway copy (--at for a point in time)
mcloud db branch list shop
mcloud db recover shop --from 3     # unblock: restore backup generation 3

mcloud secrets set STRIPE_KEY       # in the app's repo: asks for the value
mcloud secrets list                 # names only
mcloud secrets unset STRIPE_KEY
mcloud apps delete shop                             # remove it, namespace and data included

mcloud status shop        # deploy, services and URLs, workloads, databases, problems
mcloud logs shop          # logs of every pod
mcloud logs shop web -f   # follow one service, including pods that start later
mcloud generate           # what deploy/k8s would become (asks the built app)
```

Run inside an app's repo, `status` and `logs` take the app name from a
`maxcloud.yaml` there:

```yaml
app: shop
```

`create`, `set` and `delete` show the change and ask before committing, since
a commit to the homelab repo is a deploy. Pass `--dry-run` to only see it, or
`--yes` to skip the question. `delete` asks you to type the app's name.

## Releasing

Each part has its own tags; pushing one releases it.

| Tag | Releases | How |
| --- | --- | --- |
| `v0.2.0` | `mcloud` binaries on GitHub Releases | `release.yaml` (GoReleaser) |
| `functions-js/v0.2.0` | `@max-cloud/functions` on npm, after bumping `packages/functions/package.json` | `release-npm.yaml` (trusted publishing, no token) |
| `functions/v0.2.0` | the Go SDK | the tag is the release (Go modules) |

`run-deploy.yaml` pins the `mcloud` it uses (`MCLOUD_VERSION`), and Renovate
bumps it after each release.

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

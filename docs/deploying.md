# Deploying an app

An app deploys one of two ways:

- **GitOps** (the default): every push to `main` builds the app and Flux
  deploys it. The cluster can rebuild the app from git.
- **Direct**: `mcloud deploy` builds the working tree and applies it
  itself. No git, no history; for quick functions and experiments.

## GitOps

1. **Set up the repo.** `mcloud init` writes a starter functions app (or
   adds what an existing Node.js or Go app needs), its deploy workflow and
   a [`maxcloud.yaml`](maxcloud-yaml.md): the whole app's definition, like
   a `wrangler.toml`.
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

## Direct

```fish
mcloud init --deploy direct                 # a starter with deploy: direct in maxcloud.yaml
mcloud deploy                               # register, build here, push, apply, wait until ready
```

`mcloud deploy` builds the working tree, uncommitted changes included, with
`mcloud build`, the same build CI runs: with the Dockerfile, or with
buildpacks (`pack`, or pack in a container if only docker is installed).
With neither docker nor pack, an app without a Dockerfile builds in the
cluster instead (`--build cluster`, chosen by itself; with pack but no
docker, pass it yourself): a buildpacks pod in the app's namespace, with
the source streamed to it. The image goes to the maxcloud
registry with a token of the app's push ServiceAccount that lasts an hour
(whoever may manage the app's namespace may push its images); mcloud is
docker's credential helper while it builds, so there's no docker login and
nothing is written to `~/.docker`. Then it asks the image for its
functions and applies their manifests. Functions you removed are deleted;
the database never is.

What you give up: nothing is in git, so a cluster rebuild doesn't bring a
direct app back (run `mcloud deploy` again), and there's no history to
revert to. Use GitOps for anything that matters: remove `deploy: direct`
from `maxcloud.yaml` and `mcloud deploy`, which asks before it switches.

`image:` in `maxcloud.yaml` pushes a local build somewhere else instead
(log in to it yourself); the cluster then needs to be able to pull from
there. `--build cluster` refuses it, and the GitOps workflow ignores it:
those always push to the maxcloud registry.

## What a function becomes

| Function | Runs as |
| --- | --- |
| `onRequest` | a Knative service: scales to zero, private unless `public: true` |
| `onSchedule` | a CronJob |
| `onQueue` | a Deployment that KEDA scales from 0 on the queue's backlog |

A public HTTP function is at `https://<function>-<app>.gronblom.dev/`;
[custom domains](domains.md) add hostnames of your own. Queues keep their
jobs in the app's Postgres database: `db: true` in `maxcloud.yaml` (mcloud
says so when an app with a queue has none).

How to write functions: the SDKs' READMEs,
[`@max-cloud/functions`](../packages/functions/README.md) for Node.js and
TypeScript and [`functions`](../functions/README.md) for Go.

A Nuxt app can be a function too: [`@max-cloud/nuxt`](../packages/nuxt/README.md)
builds its server as one HTTP function (and its scheduled tasks as
scheduled ones), so the deploy workflow writes its manifests.

Apps that aren't functions start from the homelab repo's
`templates/run-app` and use the same workflow without `functions: true`.
The homelab README's "Run an app" covers the platform side.

## Seeing how it's doing

```fish
mcloud status                       # deploy, services and URLs, workloads, databases, problems
mcloud logs -f                      # logs of every pod; mcloud logs shop web -f for one service
mcloud call api /health             # a request to a function, private ones too
mcloud run nightly                  # a scheduled function, now
mcloud queue ls                     # jobs per queue and state, latest failed ones
mcloud queue retry 41 42            # run failed jobs again (or --failed)
```

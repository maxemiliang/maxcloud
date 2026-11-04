# maxcloud

Deploy apps and functions to my homelab, the way you would to Cloud Run or
Firebase. maxcloud is Knative on the homelab cluster, deployed through Flux
from [maxemiliang/homelab](https://github.com/maxemiliang/homelab).

This repo holds:

- `mcloud`, the command line (Go, `cmd/mcloud`). Available now.
- `@max-cloud/functions`, the Node.js/TypeScript SDK for writing HTTP,
  scheduled and queue functions (`packages/functions`, see its README).
- A Go SDK. Planned.

## How an app gets deployed

1. **Set up the repo.** `mcloud init` writes a starter functions app (or
   adds what an existing Node.js app needs), its deploy workflow and a
   `maxcloud.yaml`.
2. **Register the app.** `mcloud apps create` (or `init --register`)
   commits one entry to the homelab repo's app registry
   (`apps/maxcloud/apps/<name>.yaml`). From that entry, Flux creates the
   app's namespace, with a quota and guardrails, and a reconciler for the
   app's `deploy/k8s`.
3. **Push to deploy.** Every push to `main` runs the deploy workflow:
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
`templates/run-app` is the starting point for apps that aren't functions.

## Install

```fish
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
mcloud init                                         # in a new repo: a functions app, ready to deploy
mcloud apps create shop --repo maxemiliang/shop     # register it (once)
mcloud deploy                                       # push, follow build, Flux and rollout

mcloud apps list                                    # registered apps, did the last deploy apply
mcloud apps set shop --cpu 500m --memory 2Gi        # change its quota
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

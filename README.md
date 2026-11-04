# maxcloud

Deploy apps and functions to my homelab, the way you would to Cloud Run or
Firebase. maxcloud is Knative on the homelab cluster, deployed through Flux
from [maxemiliang/homelab](https://github.com/maxemiliang/homelab).

This repo will hold:

- `mcloud`, the command line (Go, `cmd/mcloud`). Available now.
- `@max-cloud/functions`, the Node.js/TypeScript SDK for writing functions.
  Planned.
- A Go SDK. Planned.

## How an app gets deployed

1. **Register the app.** `mcloud apps create` commits one entry to the
   homelab repo's app registry (`apps/maxcloud/apps/<name>.yaml`).
2. **Flux sets it up.** From that entry, Flux creates the app's namespace,
   with a quota and guardrails, and a reconciler that applies the app's
   `deploy/k8s` from its own repo.
3. **Pushes deploy.** Every push to the app's repo builds an image (with its
   Dockerfile or buildpacks), pins it in `deploy/k8s`, and Flux deploys it.

The homelab README's "Run an app" covers the platform side, and its
`templates/run-app` is the starting point for an app's repo.

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
mcloud apps list                                    # registered apps, did the last deploy apply
mcloud apps create shop --repo maxemiliang/shop     # register an app
mcloud apps set shop --cpu 500m --memory 2Gi        # change its quota
mcloud apps delete shop                             # remove it, namespace and data included

mcloud status shop        # deploy, services and URLs, workloads, databases, problems
mcloud logs shop          # logs of every pod
mcloud logs shop web -f   # follow one service, including pods that start later
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
```

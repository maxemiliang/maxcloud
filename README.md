# maxcloud

Deploy apps and functions to my homelab, the way you would to Cloud Run or
Firebase. maxcloud is Knative on the homelab cluster, deployed through Flux
from [maxemiliang/homelab](https://github.com/maxemiliang/homelab).

This repo holds:

- `mcloud`, the command line (Go, `cmd/mcloud`).
- `@max-cloud/functions`, the Node.js/TypeScript SDK for writing HTTP,
  scheduled and queue functions (`packages/functions`, see its README).
- `@max-cloud/nuxt`, a Nuxt module: runtimeConfig from what the platform
  gives the app, and the Nuxt server as a maxcloud function
  (`packages/nuxt`, see its README).
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
git add -A && git commit -m "maxcloud app"
gh repo create --source . --push --private
mcloud deploy                             # register it, push, follow the build and the rollout
```

## Docs

Guides:

- [Deploying an app](docs/deploying.md): GitOps and direct deploys, what
  each kind of function becomes
- [maxcloud.yaml](docs/maxcloud-yaml.md): the app's definition, and what a
  developer may ask for
- [Local development](docs/local-development.md): `mcloud dev`
- [Buckets](docs/buckets.md), public ones included
- [Databases](docs/databases.md): backups, restores, branches, migrations
- [Custom domains](docs/domains.md)
- [Environment variables](docs/environment.md): `env:` and `mcloud env`
- [Secrets and key-value storage](docs/secrets.md)
- [mcloud](docs/mcloud.md): installing, logging in, which app a command
  acts on

Reference, generated from the code:

- [mcloud commands](docs/reference/cli/README.md)
- [maxcloud.yaml fields](docs/reference/maxcloud-yaml.md)
- [Custom resources](docs/reference/crds.md): Bucket, Database,
  DatabaseBranch, Domain

Writing functions: [`@max-cloud/functions`](packages/functions/README.md)
(Node.js, TypeScript), [`functions`](functions/README.md) (Go),
[`@max-cloud/nuxt`](packages/nuxt/README.md). Running the platform:
[`controller/README.md`](controller/README.md). Working on maxcloud itself
(development, docs, releasing): [`CONTRIBUTING.md`](CONTRIBUTING.md).

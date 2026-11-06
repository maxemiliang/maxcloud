# maxcloud.yaml

`maxcloud.yaml`, at the root of the app's repo, is the whole app's
definition, like a `wrangler.toml`. Every field, its type and default:
[the reference](reference/maxcloud-yaml.md).

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/maxemiliang/maxcloud/main/maxcloud.schema.json
app: shop
quota: {cpu: 500m, memory: 2Gi}
buckets: [uploads, assets:public]
db: {size: 2Gi}          # or db: true
domains:                 # hostnames of your own, within what an admin allowed
  - {host: shop.gronblom.dev, service: web, proxied: true}
env:                     # plain variables for every function (environment.md)
  LOG_LEVEL: info
# source: worker         # build the app from this folder, not the repo's root
# deploy: direct         # mcloud deploy builds and applies from here, no git
```

The first line is optional: with it, an editor with the YAML language
server (VS Code's YAML extension, for one) completes and checks the file.

## What it owns

`mcloud deploy` applies the whole file to the app's registry entry. What
it leaves out stays as it is.

`deploy`, `quota`, `buckets`, `db` and `domains` are the file's: once it
declares one, `mcloud apps set` refuses to change it, since the next push
or deploy would undo it. For a GitOps app a push applies `quota`,
`buckets`, `db` and `domains` too, without mcloud: the controller reads
`maxcloud.yaml` at the repo's root from Flux's copy of the repo.

`deploy` takes effect on the next `mcloud deploy` only. Changing it
switches the app between GitOps and direct, which `mcloud deploy` asks
about first. Without `deploy:`, the app keeps the mode it has: GitOps for
a new app, or what `mcloud deploy --direct` or `mcloud apps set --deploy`
last switched it to. `repo`, `path` and `branch` also take effect on the
next `mcloud deploy` only, and `mcloud apps set` can still change them.
`env` reaches the functions with the next deploy (it's written into their
manifests), and `mcloud env set` refuses the names it declares
([environment variables](environment.md)). `source` and `image` are only
read by mcloud when it builds. The deploy workflow `mcloud init` writes runs on
pushes to `main`: with another `branch:`, change its trigger too.

A `maxcloud.yaml` asking for more than a developer may have is refused.
`mcloud deploy` says why; for a push, the reason is only on the entry in
`flux-system` (the annotation `maxcloud.gronblom.dev/sync-error`, and a
`SyncFailed` Warning event), which mcloud doesn't show yet. Without an
admin, an app gets at most:

| Field | At most |
| --- | --- |
| `quota.cpu` | 1 |
| `quota.memory` | 4Gi |
| `quota.pods` | 40 |
| `db.size` | 10Gi |
| `db.instances` | 2 |

The homelab repo's `infrastructure/configs/run/registry-admission.yaml`
sets these.

## Names

- `app`: lowercase letters, digits and single dashes, starting with a
  letter, at most 30 characters. Platform namespaces (`flux-system`,
  `traefik`, `kube-*`, ...) are taken.
- `buckets`: lowercase letters, digits and single dashes. `db` is the
  database's backups. The full name, `mc-<app>--<bucket>`, has to fit in
  S3's 63 characters.

## source

`source:` is the folder the app lives in, relative to `maxcloud.yaml`
(default: the folder `maxcloud.yaml` is in). Use it when the repo's root is
something else, e.g. a Go CLI at the root with the web app in `worker/`:
buildpacks would otherwise detect Go. Everything that builds or runs the
app uses it: `mcloud build`, direct deploys (local and `--build cluster`,
which streams only that folder), the deploy workflow (unless its `context`
input says otherwise), `mcloud generate` and `mcloud dev`. A Dockerfile,
`project.toml` and `migrations/` are looked for there. It has to be a
directory inside the repo, without `..`. The manifests stay where they are
(`deploy/k8s` at the root).

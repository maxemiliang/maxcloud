# Environments and previews

Besides production, an app can have:

- **environments**: long-lived copies, like staging, each deployed from a
  branch of its own;
- **previews**: short-lived copies of a pull request, or of your working
  tree, that go away by themselves.

Each is an app of its own on the cluster, named `<app>--<env>`
(`shop--staging`, `shop--pr-42`): its own namespace, URLs, secrets,
variables, buckets and database, with the app's owners. Nothing of the
app's production data, secrets or buckets is shared with it.

| | Production | `staging` | Preview of pull request 42 |
| --- | --- | --- | --- |
| Namespace | `shop` | `shop--staging` | `shop--pr-42` |
| A function's URL | `web--shop.makiya.dev` | `web--shop--staging.makiya.dev` | `web--shop--pr-42.makiya.dev` |
| Bucket `uploads` | `mc-shop--uploads` | `mc-shop--staging--uploads` | `mc-shop--pr-42--uploads`, deleted with it |
| Database | the app's | its own, backed up | empty, deleted with it (`previews.db`) |
| Deployed from | `main`: `deploy/k8s` | `staging`: `deploy/env/staging` | `maxcloud/preview/42` |

The other commands reach one with `--env` (or `$MCLOUD_ENV`):

```fish
mcloud status --env staging
mcloud logs --env pr-42 -f
mcloud secrets set --env staging STRIPE_KEY
mcloud env set --env staging LOG_LEVEL=debug
mcloud db psql --env staging
mcloud call --env pr-42 api /health
mcloud run --env staging nightly
mcloud queue ls --env staging
```

`mcloud status` on the app lists its environments and previews.

## Environments

Declare them in [`maxcloud.yaml`](maxcloud-yaml.md):

```yaml
app: shop
buckets: [uploads]
db: true
environments:
  staging:                       # branch staging, deploy/env/staging
    quota: {cpu: 100m, memory: 512Mi}
    env: {LOG_LEVEL: debug}      # over the app's env:
  qa:
    branch: release/qa
    db: false
    buckets: []
```

What an environment leaves out is the app's: its quota, its bucket names
and database settings (each environment gets empty buckets and an empty
database of its own). It never inherits `domains`; give it its own, which
an admin allows for `shop--staging` as for any app. At most 3 per app;
names are up to 12 lowercase letters, digits and dashes, not `pr-*`.

`mcloud deploy` (or, for a GitOps app, a push of `maxcloud.yaml` on the
app's branch) records them in the app's registry entry, and the maxcloud
controller makes each environment within a minute. Only the app's own
branch decides: an environment's branch can't add environments or change
its own settings. `environments: {}` removes them all; leaving the field
out keeps what the app has. Removing an environment deletes its namespace;
its buckets and database backups stay in Object Storage, as an app's do.

### Deploying an environment

**GitOps:** an environment deploys from its branch. Add the branch to the
deploy workflow's triggers, and push:

```yaml
on:
  push:
    branches: [main, staging]
```

The deploy workflow sees that `staging` is an environment's branch, builds
it the same way, and commits its manifests to `deploy/env/staging` on that
branch, which Flux applies in `shop--staging`. Since each branch keeps its
own manifests directory, merging `staging` into `main` (or back) never
conflicts on deploy commits. On the environment's branch, `mcloud deploy`
deploys and follows the environment by itself; `mcloud deploy --env
staging` does too, and refuses on another branch.

**Direct:** `mcloud deploy --env staging` builds this working tree and
applies it in `shop--staging`.

A cluster rebuild brings environments back like apps: from git, the
registry's copy and their own backups.

## Previews

Turn them on in `maxcloud.yaml`:

```yaml
previews: true
# or
previews:
  ttl: 48h        # how long one lives after its last deploy (default 72h, at most 168h)
  max: 2          # at once (default 2, at most 3); more wait
  db: true        # an empty database in each (migrations run on start)
  env: {PAYMENTS: sandbox}
```

A preview has the app's bucket names (empty buckets of its own), a small
quota (100m CPU, 512Mi, 10 pods), and none of the app's secrets: set the
ones it needs with `mcloud secrets set --env <name>`. Its HTTP functions
scale from zero to one instance, queue workers to one, and scheduled
functions don't run on their schedule (`mcloud run --env <name>` runs
one). When it expires or is deleted, its namespace, buckets and database
go with it, and its images are gone from the registry two weeks later.

### Pull requests (GitOps apps)

Label a pull request `preview`. Its next push (or the label itself) runs
the deploy workflow, which builds the pull request into
`registry.gronblom.dev/shop/shop--preview` and pushes its manifests to the
branch `maxcloud/preview/42`; Flux deploys them as `shop--pr-42`, at
`https://<function>--shop--pr-42.makiya.dev/`. Every push builds it again
and moves its expiry on. Closing the pull request, or taking the label
off, removes the preview; a preview that expires comes back with the pull
request's next push.

The deploy workflow needs the pull request trigger; `mcloud init` writes
it:

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, labeled, closed]
jobs:
  deploy:
    if: github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'preview')
```

Pull requests from forks get no preview: GitHub gives their runs no
credentials for the registry, and the workflow refuses
`pull_request_target`, which would.

### Your working tree (any app)

```fish
mcloud preview deploy            # as your login: https://web--shop--ann.makiya.dev/
mcloud preview deploy checkout   # https://web--shop--checkout.makiya.dev/
mcloud preview list
mcloud preview delete checkout
```

`mcloud preview deploy` builds the working tree, uncommitted changes
included, the way a direct deploy does, and applies it in its preview.
Deploying again replaces it and moves its expiry on. Only the app's owners
can make one.

## Who can do what

- Environments and previews exist only because the app's own registry
  entry says so (its `maxcloud.yaml` on its branch, or its owners), and only
  the maxcloud controller makes them: nobody can register `shop--anything`
  by hand.
- Each gets at most what a developer could ask for, and previews are
  capped per app and on the platform.
- A pull request's preview takes someone who can label the pull request
  and a run of the deploy workflow in the repo itself. What it can reach:
  its own empty buckets, database and secrets, and the app's images; not
  the app's secrets, database, buckets or private services.

How it's built: [the controller's README](../controller/README.md#environments-and-previews).

# maxcloud-controller

Gives apps on maxcloud the storage the app registry asks for, the way
Cloudflare gives a Worker its R2 bucket and D1 database:

- **Buckets** (R2): S3 buckets in UpCloud Managed Object Storage.
- **Databases** (D1): a CloudNativePG cluster per app, backed up
  continuously to the app's own bucket, and restored from those backups by
  itself when its cluster is gone.

It's a separate Go module, so mcloud and the SDKs don't pull in Kubernetes
and AWS libraries.

## What an app gets

The registry (homelab `apps/maxcloud`) creates a `Bucket` in the app's
namespace for every bucket in the app's entry. Apps can read these but not
create them: what storage an app has is the registry's call.

```yaml
apiVersion: maxcloud.gronblom.dev/v1alpha1
kind: Bucket
metadata: {name: uploads, namespace: shop}
spec:
  public: false          # true: anyone can GET its objects
  reclaimPolicy: Retain  # or Delete: what happens to the bucket when this goes
```

For it, the controller makes:

| | Name | |
| --- | --- | --- |
| Bucket | `mc-shop--uploads` | in the `maxcloud-apps` Object Storage instance |
| S3 user and policy | `mc-shop` | read/write objects in `mc-shop--*`; can't create, delete or reconfigure buckets |
| Secret | `maxcloud-s3` in `shop` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, UpCloud's checksum settings, `MAXCLOUD_APP`, and each bucket's full name as `MAXCLOUD_BUCKET_<NAME>` (`MAXCLOUD_BUCKET_UPLOADS=mc-shop--uploads`) |

The user and the Secret belong to the app, not to one bucket: the first
`Bucket` creates them, the last one to go removes them. Since the Secret
holds the variables every AWS SDK reads, a pod only needs
`envFrom: [{secretRef: {name: maxcloud-s3}}]`.

**Why `--`:** app names and bucket names never contain `--`, so
`mc-shop--*` (the app's policy) can't match the buckets of an app called
`shop-x` (`mc-shop-x--*`). With a single hyphen it would.

`kubectl get maxcloud -A` shows each bucket's name, size and readiness
(not `kubectl get buckets`: in a cluster with Flux, that's Flux's Bucket
kind; `buckets.maxcloud.gronblom.dev` also works). A
Bucket that isn't Ready says why in its `Ready` condition.

### Public buckets on their own hostname

With `MAXCLOUD_PUBLIC_DOMAIN` set, a `public: true` bucket also gets
`https://<bucket>--<app>.<domain>/` (in `status.url`), on the cluster's
Gateway: behind the CDN in front of the cluster, with its wildcard
certificate. GET and HEAD only. The Gateway API can't route to a host
outside the cluster (Traefik refuses ExternalName Services as HTTPRoute
backends), so the route goes through Traefik's own objects, all in the
controller's namespace:

```
HTTPRoute <bucket>--<app>.<domain>, GET/HEAD
  -> TraefikService (https, passHostHeader: false)
    -> Service type ExternalName <bucket>.<S3 host>:443
```

Traefik needs `providers.kubernetesCRD.allowExternalNameServices: true`,
`providers.kubernetesCRD.safeNaming: false` (the route names the
TraefikService by Traefik's legacy, normalized name) and the controller's
namespace in `providers.kubernetesGateway.crossProviderNamespaces`. Apps can make
neither kind of object, nor routes in that namespace. `--` can't occur in
app or bucket names, so the hostname can't collide with a Knative
service's `<svc>-<app>`. The bucket is still public on the S3 endpoint too.

### Custom domains

The registry's `domains` field makes a `Domain` per hostname in the app's
namespace, named after the hostname:

```yaml
apiVersion: maxcloud.gronblom.dev/v1alpha1
kind: Domain
metadata: {name: paja.lankkupelit.fi, namespace: paja}
spec:
  service: web     # a Knative service in the namespace (a function's name)
  proxied: true    # external-dns's cloudflare-proxied annotation
```

The controller makes an HTTPRoute `domain-<hostname>` next to it, owned by
it, on the Gateway `MAXCLOUD_DOMAINS_GATEWAY` (default `traefik/public`):

- Its rules are a copy of the route net-gateway-api makes for the
  service's cluster-local name (`<svc>.<app>.svc.cluster.local`), without
  the prober's rule. Those send each request to a revision's Service with
  the headers the activator needs, so the Domain works for private
  services and from zero, in one hop, with the client's Host. When Knative
  changes its route (a new revision), the controller copies it again.
- The listener is the Gateway's HTTPS listener for the hostname: one for
  exactly that name, else the longest wildcard that covers it one level
  deep, as its certificate does.

It routes a hostname only while:

- the allowlist ConfigMap (`MAXCLOUD_DOMAINS_ALLOWLIST` in the
  controller's namespace, default `maxcloud-domains`) has it under the
  app's key: comma-separated hostnames, or `*.zone` for one level below
  zone. Admission checks the same list when the entry is written; this
  takes the route down when an admin removes a hostname later (within
  the 5-minute resync);
- a listener covers it (else `NoListener`);
- the service has a route (else `ServiceNotFound`);
- no other HTTPRoute on the Gateway has the hostname (else `HostInUse`,
  looked at again every minute): the Gateway API gives a hostname to the
  oldest route, so a second one would silently get nothing.

`status.url`, `status.listener` and `status.revisions` show where it
routes. Why not a Knative DomainMapping: it needs a cluster-scoped
ClusterDomainClaim per hostname, and it rewrites the Host to the
cluster-local name through a second Gateway hop.

### Databases

The registry's `db` field makes a `Database` named `db` in the app's
namespace:

```yaml
apiVersion: maxcloud.gronblom.dev/v1alpha1
kind: Database
metadata: {name: db, namespace: shop}
spec:
  instances: 1           # 2: a replica on the other node
  size: 1Gi              # can grow, never shrink
  backup: {schedule: "0 0 5 * * *", retention: 7d}
  reclaimPolicy: Retain  # what happens to the backups when this goes
```

The controller turns it into a CloudNativePG `Cluster` `db` (so
`DATABASE_URL` comes from the Secret `db-app`, as for any CloudNativePG
database), an `ObjectStore` writing WAL and base backups to the bucket
`mc-shop--db` with the app's credentials, a daily `ScheduledBackup`, and a
first `Backup` for every new generation. It won't take over a `Cluster` db
it didn't create (say, from a `database.yaml` mcloud wrote before
v0.5.0): it
stops with a Blocked condition instead.

`kubectl get maxcloud -A` shows the phase (Creating, Restoring, Ready,
Blocked) and the backup generation.

### The app registry's copy

mcloud registers apps in the cluster (ResourceSetInputProviders in
`flux-system`, labelled `app.kubernetes.io/managed-by: mcloud`), not in
git. So that a rebuilt cluster gets them back, the controller copies each
one to the bucket `maxcloud-registry` (`apps/<name>.yaml`) and, when it
starts and every 15 minutes, creates any entry that has a copy but isn't in
the cluster. The registry then recreates the app, and its database restores
itself from its backups. A finalizer on each entry deletes the copy first
when the app is deleted on purpose, so a deleted app doesn't come back.
Entries that live in the homelab repo aren't copied: git has them.

### App secrets

mcloud keeps an app's secrets in the Secret `app-secrets` in its namespace,
labelled `app.kubernetes.io/managed-by: mcloud`. The cluster holds the only
copy, so with `MAXCLOUD_SECRETS_AGE_KEY` set (an age identity,
`AGE-SECRET-KEY-1...`), the controller keeps one in the registry's bucket
too: `secrets/<app>.age`, the Secret's data as JSON (`{"KEY":"value"}`),
encrypted to that key. It writes the copy when the
data changes. An empty Secret is copied as `{}`: no secrets is a state
like any other.

When an app's namespace exists and has no `app-secrets`, the controller
creates it from the copy, with mcloud's label and
`kustomize.toolkit.fluxcd.io/prune: disabled`. It looks when a tenant
namespace appears, when the Secret is deleted, and in the restore loop
after the registry's entries (at start and every 15 minutes). So after a
rebuild the secrets come back with the app, and deleting `app-secrets` by
hand brings it straight back from the copy: to remove secrets, unset them
with mcloud.

Deleting the app deletes the copy, with the registry entry's copy (the same
finalizer), so an app made later under the same name starts without
secrets. A Secret whose app has no registry entry isn't copied.

The controller caches only Secrets labelled
`app.kubernetes.io/managed-by: mcloud`; it reads any other Secret straight
from the API server. Without the key there's no copy and no restore. Keep
the key somewhere other than the cluster: without it, the copies can't be
read. Make one with `age-keygen`.

### Pulling images from the maxcloud registry

With `MAXCLOUD_IMAGE_REGISTRY` set (e.g. `registry.gronblom.dev`), every app
namespace gets credentials for that registry, and no passwords exist:

- a ServiceAccount `maxcloud-image-pull` that nothing runs as;
- the Secret `maxcloud-registry` (a docker config) holding a token of that
  ServiceAccount, with the registry's host as its audience. The controller
  renews it at half its lifetime (asked for: 7 days,
  `MAXCLOUD_IMAGE_PULL_TOKEN_LIFETIME`; the API server may grant less);
- the namespace's `default` ServiceAccount lists the Secret in
  `imagePullSecrets`, next to any it already had, so pods and Knative
  services pull with it.

Each app also gets a ServiceAccount `maxcloud-image-push`: mcloud pushes a
direct app's images with a token of it, which only someone who may manage
the app's namespace can get (Kubernetes' `admin` and `edit` roles may
create ServiceAccount tokens).

The registry (zot in the homelab) trusts the cluster's ServiceAccount
tokens for that audience and calls them `<app>:pull` and `<app>:push`. The
controller's RBAC lets it mint tokens for `maxcloud-image-pull` and no
other ServiceAccount.

### The registry's access rules

With `MAXCLOUD_ZOT_NAMESPACE` set (e.g. `registry`), the controller writes
zot's configuration: the base from the ConfigMap `zot-base` there (which
the cluster's repo owns), plus one rule per app in the app registry:

| `<app>/**` | read | push |
| --- | --- | --- |
| `<app>:pull` (the app's pull Secret) | yes | |
| `<app>:push` (mcloud, direct deploys) | yes | yes |
| `github:<repo>` (GitHub Actions in the entry's repo) | yes | yes |

into the ConfigMap `zot-config`, which zot runs with. So a repo can push
only to the apps whose registry entry names it, and an app reads only its
own images. zot reloads the rules by itself. A change to the base (logins,
storage) it reads only at start: the controller then restarts zot, by a
hash of the base on the Deployment's pod template. A rule in the base for
an app's pattern wins over the generated one.

The cluster provides a Role in that namespace for the controller: get the
ConfigMaps, create and update `zot-config`, get and patch the Deployment
`zot`.

### maxcloud.yaml from the app's repo

A GitOps app's `maxcloud.yaml` takes effect on push, with no credentials in
CI: Flux already fetches the app's repo (the GitRepository its entry
generates, which has to include `/maxcloud.yaml`). When its revision
changes, the controller downloads the artifact from Flux's
source-controller, checks its digest, and writes the file's quota, buckets,
database and domains into the app's entry. The rest of the entry (repo, path,
deploy mode) stays what mcloud registered.

It writes as the user `maxcloud:app-sync` (impersonated), not as itself,
so the cluster's limits on what a developer may ask for apply: a
`maxcloud.yaml` asking for more is refused, and the entry gets the reason
in the annotation `maxcloud.gronblom.dev/sync-error` and a Warning event.
`maxcloud.gronblom.dev/synced-revision` is the last revision handled.
Entries in the homelab repo are left alone.

`maxcloud.gronblom.dev/synced-fields` lists the entry's keys the file
declares, sorted: from `cpu`, `memory`, `pods`, `buckets`, `db`, `dbSize`,
`dbInstances` and `domains`. `quota: {cpu: 1}` declares `cpu`; `buckets: [...]`
(an empty list too) declares `buckets`; `db` in any form (`true`, `false`
or a map) declares all three database keys, since the file then owns the
database setting. mcloud refuses to set these by hand: the next push would
undo it. The annotation is set for each revision handled, and removed when
the revision has no `maxcloud.yaml`. A revision that fails to sync leaves
it as it was.

### The discovery document

So that a new machine can log in without an admin kubeconfig, the
controller serves `GET /.well-known/maxcloud.json` on `:8082`
(`MAXCLOUD_DISCOVERY_ADDR`), the Service `maxcloud-controller`'s port
`discovery`. It's public and holds nothing secret:

```json
{
  "apiServer": "https://xyz.k8s.upcloud.com:6443",
  "certificateAuthorityData": "<base64 of the CA's PEM>",
  "oidc": {"issuer": "https://...", "clientID": "..."},
  "imageRegistry": "registry.gronblom.dev",
  "mcloud": {"minVersion": "0.9.0"}
}
```

It's served only with `MAXCLOUD_API_SERVER` set (the API server's external
URL), by every replica. The CA is `ca.crt` from the ConfigMap
`kube-root-ca.crt` in the controller's namespace, read again every 5
minutes. `oidc` comes from `MAXCLOUD_OIDC_ISSUER` and
`MAXCLOUD_OIDC_CLIENT_ID` (left out without an issuer), `imageRegistry`
from `MAXCLOUD_IMAGE_REGISTRY`, and `mcloud.minVersion` from
`MAXCLOUD_MIN_MCLOUD_VERSION` (left out if unset). Clients may cache it
for 5 minutes. The public route to it lives in the cluster's repo.

## Running it

From a tag, a cluster applies [`config/`](config/kustomization.yaml): the
CRDs, RBAC and the Deployment. The cluster provides the namespace
`maxcloud-system` and, in it:

- Secret `maxcloud-controller`:
  - `UPCLOUD_TOKEN`: an API token of a sub-account whose only permission is
    the Object Storage instance;
  - `UPCLOUD_TOKEN_EXPIRES_AT` (RFC 3339), published as
    `maxcloud_upcloud_token_expiry_timestamp_seconds` so an alert can warn
    before it runs out;
  - `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`: the controller's own S3
    user, with full access to that instance (bucket policies);
  - optionally `MAXCLOUD_SECRETS_AGE_KEY`, which turns on the
    [app secrets' copies](#app-secrets).
- ConfigMap `maxcloud-controller`: `MAXCLOUD_OBJECT_STORAGE_ID` (the
  instance's UUID), `MAXCLOUD_S3_ENDPOINT` (`https://...`) and optionally
  `MAXCLOUD_S3_REGION` (default `europe-1`), `MAXCLOUD_POSTGRES_IMAGE`,
  and `MAXCLOUD_DB_STORAGE_CLASS` for the databases' disks. Prefer a class
  that deletes its disks: a restore throws the old disk away (the bucket is
  what keeps the data), and a retaining class leaves one behind each time.

For [custom domains](#custom-domains): the ConfigMap `maxcloud-domains`
(without it no Domain is routed), and optionally
`MAXCLOUD_DOMAINS_GATEWAY` and `MAXCLOUD_DOMAINS_ALLOWLIST` in the
ConfigMap above.

`MAXCLOUD_IMAGE_REGISTRY` (optional) turns on the pull credentials above.
`MAXCLOUD_API_SERVER`, `MAXCLOUD_OIDC_ISSUER`, `MAXCLOUD_OIDC_CLIENT_ID`
and `MAXCLOUD_MIN_MCLOUD_VERSION` (all optional) make the
[discovery document](#the-discovery-document).

For the sync from apps' repos the cluster also provides: a NetworkPolicy
letting `maxcloud-system` reach source-controller in `flux-system`, and a
Role there for the user `maxcloud:app-sync` to update
ResourceSetInputProviders.

It acts only in namespaces labelled `run.gronblom.dev/tenant: "true"`.
Metrics are on `:8080/metrics`, probes on `:8081`, the discovery document
on `:8082`. Metrics: `maxcloud_bucket_size_bytes`,
`maxcloud_bucket_objects` and the token's expiry.

The controller isn't on the data path: if it's down, buckets and
credentials keep working; only changes wait.

## Development

```fish
go test ./...
# After changing api/ or a +kubebuilder marker:
go tool controller-gen object paths=./api/...
go tool controller-gen crd rbac:roleName=maxcloud-controller paths=./... output:crd:dir=config/crd output:rbac:dir=config/rbac
```

CI fails if the generated files are out of date.

Running it against the real instance from a laptop needs the sub-account's
token for your IP, the controller's S3 key, and a kubeconfig; it then acts
on the cluster like the deployed one would, so scale that one to 0 first.

## Releasing

release-please does it (the root README, Releasing): its release PR bumps
`newTag` in `config/kustomization.yaml`, and merging it tags
`controller/v0.11.0` and runs `release-controller.yaml`, which builds and
pushes `ghcr.io/maxemiliang/maxcloud-controller:0.11.0` (and refuses if
`newTag` doesn't match). Clusters pick the tag in their `GitRepository`.

By hand: bump `newTag`, commit, then
`git tag controller/v0.11.0 && git push origin controller/v0.11.0`.

## How a database comes back

What decides how a database starts lives in its bucket, not in the
cluster: a marker object `maxcloud.json` (the app's policy can't write
it) with the current backup **generation** and its state. Generation N's
backups are the folder `db-N`; a restore always starts a new generation, so
older backups are never written over. So a database whose `Cluster`, or
whole namespace, or whole Kubernetes cluster is gone comes back by itself:

| `Cluster` db is missing, and | The controller |
| --- | --- |
| there's no bucket, or it's empty | a new database: generation 1, `initdb` |
| the marker's generation has a finished base backup | restores it into the next generation |
| a restore was under way and never archived anything | tries that restore again |
| the bucket has objects but no marker; or the marker's generation has WAL but no base backup; or the marker is unreadable | **Blocked**: never `initdb` over data it can't account for |

A generation counts as restorable once barman's `backup.info` in
`db-N/base/` says `status=DONE`; the controller takes that first base
backup itself as soon as the new `Cluster` is ready.

**When it's Blocked**, its `Blocked` condition says why. Look at the
bucket's generations, then pick one:

```fish
kubectl -n shop annotate database db maxcloud.gronblom.dev/recover-from=2
```

It restores that generation into a new one and removes the annotation
(`mcloud db recover --from 2` does this).

## Restoring to a point in time

```fish
kubectl -n shop annotate database db maxcloud.gronblom.dev/restore-to=2026-11-05T10:00:00Z
# or: mcloud db restore shop --at "2026-11-05 10:00"
```

The controller finds the newest generation with a finished base backup
from before that time, writes the plan (new generation, source, target
time) to the marker, takes the `Cluster` down, and restores the source up
to that time into the new generation. With the plan in the marker, an
interruption anywhere carries on with the same restore. Everything written
after the target is gone from the database, but still in the older
generation's backups. A time before the oldest backup (or in the future)
is refused: the `Restore` condition says why, and nothing is touched.

One gap: a time in the minute or two after an earlier restore, before the
new generation's first base backup, can't be reached. The old
generation's WAL ends where the new one starts, so the recovery won't
reach its target (the `MaxcloudDatabaseNotReady` alert fires).

## Branches

A `DatabaseBranch` is a throwaway copy of the database: restored from its
backups (the latest, or `spec.at`) into a `Cluster` `db-<branch>` with one
instance, no PDB and **no WAL archiving**, so it never writes into the
database's backups or marker. Its URL is in the Secret
`db-<branch>-app`. Apps may create these themselves (they count against
the app's quota); deleting the branch deletes its `Cluster`. A branch is
restored once: its spec can't change, and if its `Cluster` is deleted it
fails rather than coming back.

```fish
mcloud db branch create shop try-migration          # latest
mcloud db branch create shop yesterday --at 24h
mcloud db branch delete shop try-migration
```

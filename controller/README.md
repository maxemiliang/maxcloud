# maxcloud-controller

Gives apps on maxcloud the storage the app registry asks for, the way
Cloudflare gives a Worker its R2 bucket and D1 database:

- **Buckets** (R2): S3 buckets in UpCloud Managed Object Storage.
- **Databases** (D1): a CloudNativePG cluster per app, backed up
  continuously to the app's own bucket, and restored from those backups by
  itself when its cluster is gone.

Around those it also routes apps' custom domains, keeps copies of the app
registry and of apps' secrets in a bucket, gives apps credentials for the
image registry, writes the registry's access rules, syncs GitOps apps'
`maxcloud.yaml`, and serves a discovery document for logging in.

It's a separate Go module, so mcloud and the SDKs don't pull in Kubernetes
and AWS libraries.

## What an app gets

The registry (`platform/apps/maxcloud`) creates a `Bucket` in the app's
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
| Bucket | `mc-shop--uploads` | in the Object Storage instance `MAXCLOUD_OBJECT_STORAGE_ID` names (`maxcloud-apps` in the homelab) |
| S3 user and policy | `mc-shop` | read/write objects in `mc-shop--*`; can't create, delete or reconfigure buckets, or write the database's restore marker |
| Secret | `maxcloud-s3` in `shop` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, UpCloud's checksum settings (`AWS_REQUEST_CHECKSUM_CALCULATION` and `AWS_RESPONSE_CHECKSUM_VALIDATION`, both `when_required`), `MAXCLOUD_APP`, and each bucket's full name as `MAXCLOUD_BUCKET_<NAME>`, upper case with hyphens as underscores (`MAXCLOUD_BUCKET_UPLOADS=mc-shop--uploads`) |

The user and the Secret belong to the app, not to one bucket: the first
`Bucket` or `Database` creates them, the last of them to go removes them.
If the Secret is deleted, or its key no longer exists in UpCloud, the
controller deletes the user's keys and makes a new one (within the
5-minute resync). Since the Secret
holds the variables every AWS SDK reads, a pod only needs
`envFrom: [{secretRef: {name: maxcloud-s3}}]`.

**Why `--`:** app names and bucket names never contain `--`, so
`mc-shop--*` (the app's policy) can't match the buckets of an app called
`shop-x` (`mc-shop-x--*`). With a single hyphen it would.

A `Bucket`'s name is lowercase letters, digits and single hyphens, and not
`db` (the database's backups go to `mc-<app>--db`); the API server refuses
anything else. The controller also refuses a name ending in `-s3alias`, or
a full name over S3's 63 characters (`InvalidName`).

`kubectl get maxcloud -A` shows each bucket's name, public URL, size and
readiness (not `kubectl get buckets`: in a cluster with Flux, that's
Flux's Bucket kind; `buckets.maxcloud.gronblom.dev` also works). A Bucket
that isn't Ready says why in its `Ready` condition.

### Public buckets on their own hostname

With `MAXCLOUD_PUBLIC_DOMAIN` set, a `public: true` bucket also gets
`https://<bucket>--<app>.<domain>/` (in `status.url`), on the Gateway
listener `MAXCLOUD_PUBLIC_GATEWAY` (`namespace/gateway/listener`, default
`traefik/public/https-public`): behind the CDN in front of the cluster (the
route has external-dns's `cloudflare-proxied` annotation), with its
wildcard certificate. GET and HEAD only. The Gateway API can't route to a
host outside the cluster (Traefik refuses ExternalName Services as
HTTPRoute backends), so the route goes through Traefik's own objects, all
in the controller's namespace and named after the bucket's full name
(`mc-shop--uploads`):

```
HTTPRoute mc-<app>--<bucket>: host <bucket>--<app>.<domain>, GET/HEAD
  -> TraefikService mc-<app>--<bucket> (https, passHostHeader: false)
    -> Service mc-<app>--<bucket>, type ExternalName mc-<app>--<bucket>.<S3 host>:443
```

Making the bucket private again, or deleting it, removes the three.

Traefik needs `providers.kubernetesCRD.allowExternalNameServices: true`,
`providers.kubernetesCRD.safeNaming` the same as
`MAXCLOUD_TRAEFIK_SAFE_NAMING` (the route names the TraefikService the way
Traefik does: legacy `maxcloud-system-mc-shop-uploads`, normalized and
collision-prone, or safe `maxcloud-system_mc-shop--uploads`) and the
controller's namespace in `providers.kubernetesGateway.crossProviderNamespaces`. Apps can make
neither kind of object, nor routes in that namespace. `--` can't occur in
app or bucket names, so the hostname can't collide with another app's
bucket or Knative service (`<service>--<app>`). Within one app, a public
bucket and a public service of the same name would both want
`<name>--<app>`: name them apart. The bucket is still public on the S3 endpoint too.

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
  zone (else `NotAllowed`). Admission checks the same list when the entry
  is written; this takes the route down when an admin removes a hostname
  later (within the 5-minute resync);
- a listener covers it (else `NoListener`);
- the service has a route (else `ServiceNotFound`);
- no other HTTPRoute on the Gateway has the hostname (else `HostInUse`,
  looked at again every minute): the Gateway API gives a hostname to the
  oldest route, so a second one would silently get nothing.

In a namespace without the tenant label the reason is `NotATenant`.
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
  instances: 1           # 2: a replica, on another node if it can
  size: 1Gi              # can grow, never shrink
  backup: {schedule: "0 0 5 * * *", retention: 7d}  # cron with seconds; 7d, 2w, 1m
  reclaimPolicy: Retain  # what happens to the backups when this goes
  # resources: default 20m CPU and 128Mi requested, 512Mi limit
```

The values shown are the defaults.

The controller turns it into a CloudNativePG `Cluster` `db` (so
`DATABASE_URL` comes from the Secret `db-app`, as for any CloudNativePG
database), an `ObjectStore` `db-backups` writing WAL and base backups to
the bucket `mc-shop--db` with the app's credentials, a `ScheduledBackup`
`db-daily` on `backup.schedule`, and a first `Backup` `db-gen-<N>` for
every new generation. WAL is archived at least every 5 minutes, so that's
the most a restore of a lost database loses. With 2 instances the
`Cluster` gets a PodDisruptionBudget; with 1 it doesn't, so it never
blocks a node drain. It won't take over a `Cluster` db it didn't create
(say, from a `database.yaml` mcloud wrote before v0.5.0): it stops with a
Blocked condition instead.

`kubectl get maxcloud -A` shows the phase (Creating, Restoring, Ready,
Blocked) and the backup generation.

### The app registry's copy

mcloud registers apps in the cluster (ResourceSetInputProviders in
`flux-system`, labelled `run.gronblom.dev/app: "true"` and
`app.kubernetes.io/managed-by: mcloud`), not in git. So that a rebuilt
cluster gets them back, the controller copies each one (its labels and
spec) to the bucket `maxcloud-registry` (`MAXCLOUD_REGISTRY_BUCKET`),
as `apps/<name>.yaml`, and, when it starts and every 15 minutes (on the
leader), creates any entry that has a copy but isn't in the cluster. The
registry then recreates the app, and its database restores itself from
its backups. A finalizer on each entry deletes the copy first
when the app is deleted on purpose, so a deleted app doesn't come back.
Entries that live in git (`platform/apps/maxcloud/apps/`) aren't copied: git has them. They
get the finalizer too, for the app's secrets' and variables' copies
(below).

### Registrations: who gets an app's data

An app's buckets and database backups stay in Object Storage when its
entry is deleted (Retain), so that deleting an app doesn't delete its data
by accident. Without more, whoever registered the same name next would get
them: the app's S3 user covers every `mc-<name>--*` bucket, and the
database restores itself from `mc-<name>--db`. So:

- Every entry has a registration ID, the annotation
  `maxcloud.gronblom.dev/registration`. The controller gives an entry
  mcloud made a random one the first time it sees it, and the entry's copy
  keeps it, so an entry restored after a rebuild is the same registration.
  An entry from git (`platform/`) declares its own there (any random value,
  kept for good): the controller doesn't give those one, since Flux would
  race it, and a rebuild recreates them from git without a copy. Admission
  lets only the controller and Flux set or change it.
- The controller keeps a record per app name in the registry's bucket,
  `registrations/<name>.json`: which registration the name's data belongs
  to. Deleting the entry leaves it there.
- Before a Bucket, Database or DatabaseBranch makes or hands out anything
  (the app's credentials included), the controller checks that the app's
  entry is that registration. If the record names another one and any
  bucket `mc-<name>--*` still exists, it stops with `Ready` False, reason
  `RegistrationMismatch`, and a message with both IDs and the fix. A
  running Cluster is left as it is. If no bucket is left, or nobody has
  a record yet and no bucket exists, the entry takes the name and the
  record moves to it.
- An admin gives a new entry the old data with the annotation
  `maxcloud.gronblom.dev/adopt-registration=<the old ID>` on it (admission
  lets only admins set it). The record then moves to the new entry, with
  the old ID in its `previous`.
- When this first ran, nothing had a record. The controller wrote
  `registrations/_since.json` (when records began) when it first started;
  an entry made before then keeps the data under its name without a
  record (so fn-demo, paja and the rest carried on, database included),
  and one made after doesn't: leftovers of an app deleted before records
  began need `adopt-registration=unrecorded` from an admin.

A record the controller can't read stops every check for that app (an
error, retried), rather than being overwritten.

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

Deleting the app's registry entry deletes the copy, with the entry's own
copy (the same finalizer), whether mcloud made the entry or it's in
`platform/`, so an app made later under the same name starts without
secrets. A Secret whose app has no registry entry isn't copied.

The controller caches only Secrets labelled
`app.kubernetes.io/managed-by: mcloud`; it reads any other Secret straight
from the API server. Without the key there's no copy and no restore. Keep
the key somewhere other than the cluster: without it, the copies can't be
read. Make one with `age-keygen`.

### App env

mcloud keeps an app's plain variables (`mcloud env set`) in the ConfigMap
`app-env` in its namespace, labelled `app.kubernetes.io/managed-by:
mcloud`. The controller copies it to the registry's bucket as
`env/<app>.json` (`{"KEY":"value"}`, not encrypted: these aren't secrets,
and no key is needed) and puts it back the same way it does
`app-secrets`: when a tenant namespace appears, when the ConfigMap is
deleted, and in the restore loop. Deleting the app deletes the copy too.
The controller caches only ConfigMaps with mcloud's label.

### Pulling images from the maxcloud registry

With `MAXCLOUD_IMAGE_REGISTRY` set (e.g. `registry.gronblom.dev`), every app
namespace gets credentials for that registry, and no passwords exist:

- a ServiceAccount `maxcloud-image-pull` that nothing runs as;
- the Secret `maxcloud-registry` (a docker config) holding a token of that
  ServiceAccount, with the registry's host as its audience. The controller
  renews it at half its lifetime (asked for: 7 days, or
  `MAXCLOUD_IMAGE_PULL_TOKEN_LIFETIME`, a Go duration of at least `10m`;
  the API server may grant less), and looks at least hourly, so a deleted
  Secret comes back within the hour;
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
| `github:<repo>` (GitHub Actions in the entry's repo; not for direct-deploy apps) | yes | yes |

into the ConfigMap `zot-config`, which zot runs with. `<app>` is the
entry's `name` value, else the entry's name; every entry labelled
`run.gronblom.dev/app: "true"` counts, those from `platform/` too. So a repo
can push only to the apps whose registry entry names it, and an app reads
only its own images. zot reloads the rules by itself. The controller
reads the base again every 2 minutes. A change to the base (logins,
storage) zot reads only at start: the controller then restarts zot, by a
hash of the base on the Deployment's pod template. A rule in the base for an app's pattern
wins over the generated one.

The cluster provides a Role in that namespace for the controller: get the
ConfigMaps, create and update `zot-config`, get and patch the Deployment
`zot`.

### maxcloud.yaml from the app's repo

A GitOps app's `maxcloud.yaml` takes effect on push, with no credentials in
CI: Flux already fetches the app's repo (the GitRepository its entry
generates in `flux-system`, named like the entry and labelled
`app.kubernetes.io/part-of: maxcloud`, which has to include
`/maxcloud.yaml`). When its revision changes, the controller downloads
the artifact from Flux's
source-controller, checks its digest, and writes the file's quota, buckets,
database and domains into the app's entry. The rest of the entry (repo, path,
deploy mode) stays what mcloud registered. A file whose `app` names a
different app is refused, and so is one with values the registry can't
render (one ResourceSet renders every app, so a bad entry would stop them
all): a bucket name `naming.Bucket` refuses, `db` or a duplicate in
`buckets`, an option other than `:public`, a quota or database size that
isn't a quantity above 0, `pods` below 1 or `db.instances` outside 1-2.
The entry then keeps its values.

It never touches the entry's `owners` (who may run the app is the
registry's, not the repo's: a push mustn't grant it) or its registration
ID, and admission refuses `maxcloud:app-sync` changing either.

It writes as the user `maxcloud:app-sync` (impersonated), not as itself,
so the cluster's limits on what a developer may ask for apply: a
`maxcloud.yaml` asking for more is refused, and the entry gets the reason
in the annotation `maxcloud.gronblom.dev/sync-error` and a Warning event.
`maxcloud.gronblom.dev/synced-revision` is the last revision handled.
Entries from git (`platform/`) are left alone.

`maxcloud.gronblom.dev/synced-fields` lists the entry's keys the file
declares, sorted: from `deploy`, `cpu`, `memory`, `pods`, `buckets`, `db`,
`dbSize`, `dbInstances` and `domains`. `quota: {cpu: 1}` declares `cpu`;
`buckets: [...]` (an empty list too) declares `buckets`; `db` in any form
(`true`, `false` or a map) declares all three database keys, since the file
then owns the database setting. `deploy:` is declared but not applied here:
`mcloud deploy` switches the mode. mcloud refuses to set these by hand: the
next push or deploy would undo it. The annotation is set for each revision handled, and removed when
the revision has no `maxcloud.yaml`. A revision that fails to sync leaves
it as it was.

### The discovery document

So that a new machine can log in without an admin kubeconfig, the
controller serves `GET` (and `HEAD`) `/.well-known/maxcloud.json` on `:8082`
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
for 5 minutes (`Cache-Control: max-age=300`). If the CA can't be read and
none was read before, it answers 503. The public route to it lives in the
cluster's repo.

## Running it

From a tag, a cluster applies [`config/`](config/kustomization.yaml): the
CRDs, RBAC, the Deployment (one replica) and the Service for the discovery
document. The cluster provides the namespace `maxcloud-system` and, in it:

- Secret `maxcloud-controller`:
  - `UPCLOUD_TOKEN`: an API token of a sub-account whose only permission is
    the Object Storage instance;
  - optionally `UPCLOUD_TOKEN_EXPIRES_AT` (RFC 3339), published as
    `maxcloud_upcloud_token_expiry_timestamp_seconds` so an alert can warn
    before it runs out;
  - `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`: the controller's own S3
    user, with full access to that instance (bucket policies);
  - optionally `MAXCLOUD_SECRETS_AGE_KEY`, which turns on the
    [app secrets' copies](#app-secrets).
- ConfigMap `maxcloud-controller`: `MAXCLOUD_OBJECT_STORAGE_ID` (the
  instance's UUID), `MAXCLOUD_S3_ENDPOINT` (must start with `https://`) and
  optionally `MAXCLOUD_S3_REGION` (default `europe-1`),
  `MAXCLOUD_POSTGRES_IMAGE` (default: `DefaultPostgresImage` in
  `internal/controller/database.go`, which Renovate keeps current), and
  `MAXCLOUD_DB_STORAGE_CLASS` for the databases' disks (default: the
  cluster's default class). Prefer a class that deletes its disks: a
  restore throws the old disk away (the bucket is what keeps the data), and
  a retaining class leaves one behind each time.

The rest is optional, also in that ConfigMap, each turning on or adjusting
one part:

| Variable | Default | |
| --- | --- | --- |
| `MAXCLOUD_PUBLIC_DOMAIN` | unset: off | [public buckets' hostnames](#public-buckets-on-their-own-hostname) |
| `MAXCLOUD_PUBLIC_GATEWAY` | `traefik/public/https-public` | their Gateway listener, `namespace/gateway/listener` |
| `MAXCLOUD_TRAEFIK_SAFE_NAMING` | `false` | `true` when Traefik runs with `providers.kubernetesCRD.safeNaming: true`: how their routes name the TraefikService |
| `MAXCLOUD_DOMAINS_GATEWAY` | `traefik/public` | the Gateway for [custom domains](#custom-domains), `namespace/gateway` |
| `MAXCLOUD_DOMAINS_ALLOWLIST` | `maxcloud-domains` | the allowlist ConfigMap; without it no Domain is routed |
| `MAXCLOUD_REGISTRY_BUCKET` | `maxcloud-registry` | the bucket for the [registry's copy](#the-app-registrys-copy) and the secrets' copies |
| `MAXCLOUD_IMAGE_REGISTRY` | unset: off | the registry's host: [pull credentials](#pulling-images-from-the-maxcloud-registry) |
| `MAXCLOUD_IMAGE_PULL_TOKEN_LIFETIME` | `168h` | the pull tokens' lifetime asked for, at least `10m` |
| `MAXCLOUD_ZOT_NAMESPACE` | unset: off | zot's namespace: [its access rules](#the-registrys-access-rules) |
| `MAXCLOUD_API_SERVER` | unset: off | the API server's external URL: the [discovery document](#the-discovery-document) |
| `MAXCLOUD_DISCOVERY_ADDR` | `:8082` | where that's served |
| `MAXCLOUD_OIDC_ISSUER`, `MAXCLOUD_OIDC_CLIENT_ID`, `MAXCLOUD_MIN_MCLOUD_VERSION` | unset | in the discovery document |

The Deployment sets `POD_NAMESPACE` (default `maxcloud-system`): the
controller's namespace, where the public buckets' route objects go and the
allowlist and `kube-root-ca.crt` are read. The controller won't start
without a required variable, or with one it can't read (a gateway with the
wrong number of parts, a bad age key, lifetime or expiry time).

For the sync from apps' repos the cluster also provides: a NetworkPolicy
letting `maxcloud-system` reach source-controller in `flux-system`, and a
Role there for the user `maxcloud:app-sync` to update
ResourceSetInputProviders. Databases need CloudNativePG and its
barman-cloud plugin in the cluster.

It acts on apps only in namespaces labelled `run.gronblom.dev/tenant:
"true"`. Metrics are on `:8080/metrics` (`--metrics-bind-address`), probes
(`/healthz`, `/readyz`) on `:8081` (`--health-probe-bind-address`), the
discovery document on `:8082`. Metrics:

- `maxcloud_bucket_size_bytes` and `maxcloud_bucket_objects` (labels `app`,
  `bucket`), from UpCloud's bucket metrics;
- `maxcloud_database_phase` (labels `app`, `phase`): 1 for the database's
  current phase, 0 for the others;
- `maxcloud_database_generation` (label `app`): its backup generation;
- `maxcloud_upcloud_token_expiry_timestamp_seconds`.

`--leader-elect` (on by default) uses the Lease `maxcloud-controller`: only
the leader reconciles and runs the restore loop; every replica serves the
discovery document.

The controller isn't on the data path: if it's down, buckets and
credentials keep working; only changes wait.

It reads at most 8 MiB of any object in Object Storage (markers, the
registry's and secrets' copies, registration records, barman's
`backup.info`). All of these are small, but some sit where an app's own
S3 key can write, and a huge one would take the controller past its
memory limit (256Mi) on every start. A bigger object is an error, not
read in part.

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

release-please does it ([CONTRIBUTING.md](../CONTRIBUTING.md#releasing)): its release PR bumps
`newTag` in `config/kustomization.yaml`, and merging it tags
`controller/vX.Y.Z` and runs `release-controller.yaml`, which builds and
pushes `ghcr.io/maxemiliang/maxcloud-controller:X.Y.Z` (and refuses if
`newTag` doesn't match). Clusters pick the tag in their `GitRepository`.

By hand: bump `newTag`, commit, then
`git tag controller/vX.Y.Z && git push origin controller/vX.Y.Z`.

## How a database comes back

What decides how a database starts lives in its bucket, not in the
cluster: a marker object `maxcloud.json` (the app's policy can't write
it) with the current backup **generation** and its state. Generation N's
backups are the folder `db-N`; a restore always starts a new generation, so
older backups are never written over. So a database whose `Cluster`, or
whole namespace, or whole Kubernetes cluster is gone comes back by itself:

| `Cluster` db is missing, and | The controller |
| --- | --- |
| there's no bucket, or it's empty (or holds only the marker) | a new database: generation 1, `initdb` |
| a new database's generation never archived anything | `initdb` again, in the same generation |
| the marker's generation has a finished base backup | restores it into the next generation |
| a restore was under way and never archived anything | tries that restore again, to the same target time if it had one |
| a restore was under way and archived WAL but no base backup | restores the same source again, into a new generation |
| the bucket has objects but no marker; or a new database's generation has WAL but no base backup; or neither a restore's generation nor its source has a base backup; or the marker says ready but there's no base backup; or the marker is unreadable | **Blocked**: never `initdb` over data it can't account for |

A generation counts as restorable once barman's `backup.info` in
`db-N/base/` says `status=DONE` (one over 8 MiB is ignored); the
controller takes that first base backup itself as soon as the new
`Cluster` is ready.

**When it's Blocked**, its `Blocked` condition says why. Look at the
bucket's generations, then pick one:

```fish
kubectl -n shop annotate database db maxcloud.gronblom.dev/recover-from=2
```

It restores that generation into a new one and removes the annotation
(`mcloud db recover --from 2` does this). A generation without a finished
base backup stays Blocked. The annotation is read only while the
`Cluster` is missing: a Database Blocked by a `Cluster` `db` it doesn't
own needs that `Cluster` removed first.

## Restoring to a point in time

```fish
kubectl -n shop annotate database db maxcloud.gronblom.dev/restore-to=2026-11-05T10:00:00Z
# or: mcloud db restore shop --at "2026-11-05 10:00"
```

The controller finds the newest generation with a finished base backup
that ended before that time, writes the plan (new generation, source, target
time) to the marker, takes the `Cluster` down, and restores the source up
to that time into the new generation. With the plan in the marker, an
interruption anywhere carries on with the same restore. Everything written
after the target is gone from the database, but still in the older
generation's backups. A time it can't read (RFC 3339), one in the future,
or one before the oldest finished base backup is refused: the annotation
is removed, the `Restore` condition (reason `Refused`) says why, and
nothing is touched. A restore that goes ahead sets `Restore` to True
(reason `Restored`) and `status.restoredTo`.

If the `Cluster` is already gone when the annotation is set, the restore
stops at that time too: the plan is written and the restore runs without
the take-down. A time it can't reach is refused the same way (also when
`recover-from` is set too, which wins), and the database then comes back
as it would without it: its latest state.

One gap: a time in the minute or two after an earlier restore, before the
new generation's first base backup, can't be reached. The old
generation's WAL ends where the new one starts, so the recovery won't
reach its target (the `MaxcloudDatabaseNotReady` alert fires).

## Branches

A `DatabaseBranch` is a throwaway copy of the database: restored from its
backups (the latest, or `spec.at`) into a `Cluster` `db-<branch>` with one
instance, no PDB and **no WAL archiving**, so it never writes into the
database's backups or marker. Its URL is in the Secret
`db-<branch>-app` (key `uri`). Apps may create these themselves (they
count against the app's quota); deleting the branch deletes its
`Cluster`. A branch is restored once: its spec can't change, and if its
`Cluster` is deleted it fails rather than coming back.

```yaml
apiVersion: maxcloud.gronblom.dev/v1alpha1
kind: DatabaseBranch
metadata: {name: try-migration, namespace: shop}   # at most 30 characters
spec:
  database: db                   # the default
  at: "2026-11-05T10:00:00Z"     # optional; default the latest
  size: 2Gi                      # optional; default the database's size
```

Its phase is Creating, Ready or Failed; the `Ready` condition says why it
failed (`NoDatabase`, retried every minute; `NoBackupBefore`, when the
database has no finished base backup from before `at` yet, which also
happens to a branch of a database made minutes ago; `ClusterGone`).
Except for `NoDatabase`, a failed branch isn't tried again: delete it and
make a new one.

```fish
mcloud db branch create shop try-migration          # latest
mcloud db branch create shop yesterday --at 24h
mcloud db branch delete shop try-migration
```

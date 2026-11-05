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
| Secret | `maxcloud-s3` in `shop` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION` and UpCloud's checksum settings |

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
it didn't create (say, from a `database.yaml` in the app's repo): it
stops with a Blocked condition instead.

`kubectl get maxcloud -A` shows the phase (Creating, Restoring, Ready,
Blocked) and the backup generation.

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
    user, with full access to that instance (bucket policies).
- ConfigMap `maxcloud-controller`: `MAXCLOUD_OBJECT_STORAGE_ID` (the
  instance's UUID), `MAXCLOUD_S3_ENDPOINT` (`https://...`) and optionally
  `MAXCLOUD_S3_REGION` (default `europe-1`), `MAXCLOUD_POSTGRES_IMAGE`,
  and `MAXCLOUD_DB_STORAGE_CLASS` for the databases' disks. Prefer a class
  that deletes its disks: a restore throws the old disk away (the bucket is
  what keeps the data), and a retaining class leaves one behind each time.

It acts only in namespaces labelled `run.gronblom.dev/tenant: "true"`.
Metrics are on `:8080/metrics`: `maxcloud_bucket_size_bytes`,
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

Bump `newTag` in `config/kustomization.yaml`, commit, then:

```fish
git tag controller/v0.2.0 && git push origin controller/v0.2.0
```

`release-controller.yaml` builds and pushes
`ghcr.io/maxemiliang/maxcloud-controller:0.2.0` (and refuses if `newTag`
doesn't match). Clusters pick the tag in their `GitRepository`.

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

It restores that generation into a new one and removes the annotation.
Restoring to a point in time, and branches (a second `Cluster` from a
point in time), come later.

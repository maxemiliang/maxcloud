# maxcloud-controller

Gives apps on maxcloud the storage the app registry asks for, the way
Cloudflare gives a Worker its R2 bucket and D1 database:

- **Buckets** (R2): S3 buckets in UpCloud Managed Object Storage. Available now.
- **Databases** (D1): a CloudNativePG cluster per app, backed up to the
  app's own bucket, restored automatically when it's gone. Planned; see
  [Design](#design).

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
  `MAXCLOUD_S3_REGION` (default `europe-1`).

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

## Design

**Buckets** are as above.

**Databases (next).** A `Database` kind in the app's namespace (one per
app, named `db`, so `db-app` and `DATABASE_URL` stay as they are today).
The controller owns its CloudNativePG `Cluster`, `ObjectStore` and
`ScheduledBackup`; backups go to the app's bucket `mc-<app>--db`, with the
app's own credentials.

The state that decides how a database starts lives in that bucket, not in
the cluster: a marker object `maxcloud.json` with the current backup
generation (the app's policy can't write it). So a database whose cluster
or namespace is gone comes back from its backups by itself:

| Database `db` has no `Cluster`, and | The controller |
| --- | --- |
| there's no bucket | creates it, writes generation 1, `initdb` |
| the marker says generation N, which has a base backup | writes N+1, restores N into a new `Cluster` (backups now go to `db-N+1`) |
| the marker says N but N has no base backup yet | **Blocked**: a person picks the generation to restore |
| the bucket has objects but no marker, or any API error | **Blocked** or retry; never `initdb` |

It never writes over an older generation's backups, and never deletes a
bucket unless its reclaim policy says so. Later: branches (a new `Cluster`
from a point in time) and restores in place.

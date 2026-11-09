---
title: How maxcloud databases come back after losing their Cluster or namespace
kind: fact
area: data
tags: [maxcloud, cnpg, backup, restore, dr, storageclass]
learned: 2026-11-05
updated: 2026-11-08
where:
  - apps/maxcloud/apps/run-demo.yaml
  - infrastructure/controllers/maxcloud/config.yaml
---

# How maxcloud databases come back after losing their Cluster or namespace

The maxcloud controller restores an app's database (registry field `db`)
from its bucket `mc-<app>--db` by itself. The rules are in the maxcloud
repo, `controller/README.md`, "How a database comes back". Measured on
run-demo on 2026-11-05 (1 instance, 1Gi, a nearly empty database):

- **New database to Ready:** about 1.5 minutes. That covers the disk
  (about 25 s to provision), initdb, and the first base backup
  (`db-gen-1`, about 10 s).
- **`kubectl delete` of the Cluster:** a restore into the next generation
  starts within 5 s. It is Ready about 2 minutes later, including the
  recovery Job and the new generation's first base backup.
- **`kubectl delete namespace`:** the registry recreates the namespace,
  and the controller makes a new S3 user and key (a new `maxcloud-s3`
  Secret), reads the marker and restores. Ready about 2 minutes after the
  namespace came back.
- **Twice in a row:** a second delete about 30 s after a restore finished
  restored the newer generation into the next one (1 → 2 → 3). The row
  written in generation 1 survived all three restores (1 → 2 → 3 → 4).
- **Branch at a point in time** (`mcloud db branch create run-demo before-t
  --at <T>`, 2026-11-05): Ready in about 2.5 minutes. The recovery Job
  takes about 1.5 of those, and then CloudNativePG restarts the instance
  once to drop the backup sidecar the restore used ("Upgrading instance",
  harmless). The branch had the row from before T and not the one written
  3 seconds after; the database was untouched.
- **Restore to a point in time** (`mcloud db restore run-demo --at <T>`,
  same test): 2.5 minutes from the plan to the new generation's first base
  backup (4 → 5). Only the row from before T was left. With the `-delete`
  StorageClass there was no leftover disk.
- Every generation keeps its own recovery window (ObjectStore
  `.status.serverRecoveryWindow`: `db-1`, `db-2`, ...). Nothing is
  written over.
- **What's lost:** at most the WAL not yet archived. `archive_timeout` is
  5 minutes. `select pg_switch_wal()` archives right away (that's how the
  test made sure its row was in the backups).

**Decision: databases' disks are deleted with their Cluster**
(`MAXCLOUD_DB_STORAGE_CLASS: upcloud-block-storage-maxiops-encrypted-delete`,
since controller/v0.3.0).

- **Why:** the bucket keeps the data, and every restore starts on a new
  disk. With the default Retain class each restore left a Released disk,
  still billed, and deleting a Retain PV doesn't delete the UpCloud disk
  (`upctl storage delete <volumeHandle>`, then `kubectl delete pv`).
- **Rejected:** keeping Retain for safety. Deleting the Database by
  mistake only loses the unarchived WAL, and the backups stay (reclaim
  policy Retain on the bucket).

A `database.yaml` database (the run-app template's example, or what
mcloud wrote for queue functions before v0.5.0) isn't covered: it has no
backups. Moving one over:
[docs/runbooks/move-database-to-registry.md](../../runbooks/move-database-to-registry.md).

See also: [upcloud-object-storage-behaviour](upcloud-object-storage-behaviour.md),
[off-region-backup-copy](../ops/off-region-backup-copy.md),
[app owners and registrations](maxcloud-app-owners-and-registrations.md)
(a new entry under a deleted app's name doesn't restore its backups).

---
title: Backups stay in one region for now; a copy to de-fra1 is the plan when needed
kind: decision
area: ops
tags: [backup, dr, object-storage, upcloud, region, rclone, maxcloud, cnpg]
learned: 2026-11-05
updated: 2026-11-05
where:
  - infra/storage/maxcloud.tf
  - infra/storage/main.tf
---

# Backups stay in one region for now; a copy to de-fra1 is the plan when needed

**Decision:** not now (2026-11-05). Every backup lives in UpCloud Object
Storage region europe-1 (fi-hel2), the same place as the cluster:

- `pg-backups` and `loki` in the `homelab` instance;
- the apps' buckets and database backups (`mc-<app>--db`) in `maxcloud-apps`.

Nothing copies them anywhere else.

**What that leaves uncovered:**

- losing the region;
- losing or deleting an instance. The UpCloud API deletes buckets even when
  they still hold objects, and `prevent_destroy` only guards tofu.
- a mistake or a compromised key emptying a bucket. Object versioning and
  lifecycle are supported but not turned on; object lock isn't supported.

**The plan, when it's wanted:**

- A second Object Storage instance in `de-fra1`, in `infra/storage`. It
  costs about €5/month per 250 GB.
- A nightly `rclone sync` CronJob of both instances' buckets, with an S3
  user that can only read the source, and a separate one that writes only
  to the copy.
  - About 10m CPU, at night only.
  - UpCloud has no bucket replication, so it has to be a copy job.
- Turn on versioning on the copy, so a sync of a deleted object can be
  undone.
- Restores would point the barman-cloud ObjectStore or the maxcloud
  controller's endpoint at the copy. Write that runbook together with it.

**Why not now:** the platform is young and its data is small and
recreatable, and the remaining gaps (queue databases without backups)
matter more.

**Revisit when:** an app holds data that can't be recreated, the backups
grow past the first 250 GB, or a DR drill is planned.

See also: [maxcloud-database-recovery](../data/maxcloud-database-recovery.md).

---
title: "Block storage: MaxIOPS for databases, Standard for the rest"
kind: decision
area: data
tags: [upcloud, storageclass, pvc, maxiops, loki, prometheus, cnpg]
learned: 2026-11-12
updated: 2026-11-12
where:
  - infrastructure/controllers/storage/
  - infrastructure/controllers/kustomization.yaml
---

# Block storage: MaxIOPS for databases, Standard for the rest

**Decision:** two Delete-reclaim, encrypted classes of our own:

- `upcloud-block-storage-maxiops-encrypted-delete` for Postgres
  (`MAXCLOUD_DB_STORAGE_CLASS`) and Prometheus;
- `upcloud-block-storage-standard-encrypted-delete` for volumes that are
  written sequentially or are easy to lose: Loki's WAL, queues, caches.

Anything with data worth keeping uses a Retain class (UKS's default,
`upcloud-block-storage-maxiops-encrypted`).

**Why:** MaxIOPS reads up to 100k IOPS at about €0.22/GB a month; Standard
reaches 10k IOPS at about €0.085 (UpCloud's list prices, 2026-11). Postgres
and Prometheus's compaction do small random writes with fsync, where
latency shows. Loki's volume only holds the WAL; its data is in Object
Storage. At the 1–10 GiB we use the saving is a few euros a month, so the
default stays MaxIOPS when unsure.

**The classes live in `infrastructure/controllers/storage`, not in
configs:** `infra-configs` only applies once `infra-controllers` is
healthy, and the HelmReleases in controllers have PVCs that use these
classes. With the classes in configs, a fresh cluster (or a new class)
leaves those PVCs Pending, the HelmReleases time out, and configs never
applies.

**Rejected:** Archive (600 IOPS) is too slow for any PVC; backups go to
Object Storage instead.

**Revisit when:** tenant databases or volumes grow to hundreds of GB, or a
Standard-tier workload shows I/O wait.

See also: [A StatefulSet's storage class can't be changed](../ops/statefulset-storage-class-change.md),
[How maxcloud databases come back](maxcloud-database-recovery.md)

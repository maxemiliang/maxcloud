---
title: maxcloud apps have owners, and their data belongs to one registration
kind: decision
area: data
tags: [maxcloud, rbac, admission, authelia, buckets, cnpg, security]
learned: 2026-11-07
updated: 2026-11-07
where:
  - apps/maxcloud/resourceset.yaml
  - infrastructure/configs/run/registry-admission.yaml
  - infrastructure/configs/auth/developers.yaml
  - apps/maxcloud/apps/run-demo.yaml
---

# maxcloud apps have owners, and their data belongs to one registration

**Decision (2026-11-07, security audit finding M10):**

- A registry entry has `owners`, Authelia usernames. The ResourceSet binds
  `admin` and `maxcloud-app-developer` in the app's namespace to the Users
  `authelia:<owner>`, no longer to the group `authelia:maxcloud-developers`.
  An entry without owners (run-demo) gets no developer bindings.
- Admission (`maxcloud-registry-owners`): a developer registering an app
  must be among its owners; only owners change or delete an entry; owners
  can't become empty; `maxcloud:app-sync` (a repo's `maxcloud.yaml`) never
  changes them.
- Every entry has a registration ID (annotation
  `maxcloud.gronblom.dev/registration`). The controller records per app
  name which registration the buckets `mc-<name>--*` belong to
  (`maxcloud-registry/registrations/<name>.json`), and the record outlives
  the entry. A Bucket, Database or DatabaseBranch of another registration
  gets nothing (Ready False, `RegistrationMismatch`). An admin hands the
  data over with `maxcloud.gronblom.dev/adopt-registration=<old ID>`. Code:
  maxcloud repo, `controller/internal/controller/registration.go`.

**Why:** with the group bound everywhere, any developer could read every
app's `db-app` and `maxcloud-s3` Secrets and delete any app. And because
buckets and backups are kept (Retain) when an entry goes, whoever
registered the same name next got the S3 user that covers `mc-<name>--*`,
and a database that restored itself from the old backups.

How it's built, and why:

- **Owners in the entry, not a separate object:** the ResourceSet can
  `range` over `splitList "," (get inputs "owners")`
  (checked with `flux-operator build rset` against the live entries), so
  the bindings come from the same input as everything else.
- **Admission exempts** admins (`authelia:admins`, the admin certificate's
  `kubeadm:cluster-admins`, `system:masters`), kustomize-controller (git),
  Flux Operator (it updates entries' metadata for its finalizer: seen in
  their managedFields) and the controller. There's no `not-finalizing`
  exemption in the owners policy: only the exempt controllers remove
  finalizers, and a non-owner removing the controller's would skip
  deleting the app's secrets' copies.
- **Registration IDs:** the controller gives mcloud's entries one and keeps
  it in the entry's copy, so a rebuilt cluster restores the same
  registration. Entries from git declare theirs in git (run-demo does). The
  controller doesn't give them one, because Flux would race it, and a
  rebuild recreates them from git without a copy. Only the controller and
  kustomize-controller may set or change it, admins included.
- **Existing apps on the first rollout:** the controller writes
  `registrations/_since.json` when it first starts. An entry made before
  then keeps the unrecorded data under its name (so fn-demo, maxoo, paja
  and run-demo carried on, databases untouched). One made after doesn't:
  it needs `adopt-registration=unrecorded`. `creationTimestamp` is set by
  the API server, so an entry can't claim to be old.
- **The name is free again** once no bucket `mc-<name>--*` exists: the next
  entry takes the record.

**Rejected:**

- Deleting buckets with the entry: an `mcloud apps delete` would lose data
  for good; Retain was decided with the data plan.
- Letting the old owners re-adopt by themselves: the controller can't
  tell who made an entry, and an entry's owners are whatever its creator
  wrote, so that would be spoofable. Admins adopt.
- Telling old entries apart by the controller's finalizer: a developer can
  put any finalizer on a new entry.

**Revisit when:** apps get a non-admin way to hand data over (then make
adoption an owner action that admission can verify), or images in the
maxcloud registry (zot keeps `registry.gronblom.dev/<name>/*` after an
app is deleted) need the same protection.

See also: [The admin certificate isn't system:masters](../ops/admin-certificate-not-system-masters.md),
[maxcloud database recovery](maxcloud-database-recovery.md).

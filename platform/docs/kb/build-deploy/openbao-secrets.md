---
title: Every secret lives in OpenBao; External Secrets Operator makes the Secrets
kind: decision
area: build-deploy
tags: [openbao, external-secrets, eso, secrets, sops, maxcloud, security, trust-manager]
learned: 2026-11-22
updated: 2026-11-22
where:
  - secrets-store/
  - secrets/
  - infrastructure/controllers/external-secrets/release.yaml
  - apps/maxcloud/resourceset.yaml
  - infra/flux/openbao.tf
  - scripts/bao.sh
---

# Every secret lives in OpenBao; External Secrets Operator makes the Secrets

**Decision (2026-11-22):** OpenBao (one replica, storage in the CNPG
cluster `openbao-db`, static seal key from tofu) holds the platform's
secrets at `platform/<namespace>/<name>` and apps' at
`apps/<namespace>/app-secrets`. ESO turns them into Secrets of the same
names. It replaces SOPS files in the private repo and the maxcloud
controller's age-encrypted copies of `app-secrets`. Design and the
options weighed: `private/docs/research/secrets-openbao-eso.md`.

**Why:**
- With SOPS, one age key opened every secret, so editing any value
  needed that key, `sops` and the private repo. Now an admin edits one path
  with their Authelia login (`scripts/bao.sh`), and app owners edit only
  their own app's, through mcloud.
- Every namespace reads only its own path. Each SecretStore logs in as
  that namespace's `eso-reader` ServiceAccount, and one templated policy
  (`secrets-store/config/ns-reader.hcl`) keys the path on the namespace in
  the signed token. Tested in kind: a token from `t-b` gets
  `permission denied` on `t-a`'s path, and so does a list of the mount.
- The all-or-nothing `secrets` Kustomization no longer deadlocks the
  bootstrap order: app namespaces get their ExternalSecret from the
  registry, not from it.
- Versions, an audit log (to stdout and so to Loki) and one backup (the
  Postgres one) instead of a hand-made copy per app.

**How it's cut:**
- **Two mounts.** The controller's policy covers `apps/` only, so no
  deny-list of platform namespaces is needed (one would break silently
  with each new namespace).
- **No ESO kinds in `admin`, no cluster-scoped kinds, no PushSecret**
  (chart values). Apps can't write a SecretStore, so they can't point ESO's
  webhook provider at a cluster URL from the `external-secrets` namespace,
  which the tenant egress deny doesn't cover. ESO's egress is limited to
  the API server, DNS and OpenBao as well.
- **No route to OpenBao.** People reach it through `kubectl exec`
  (`scripts/bao.sh`), and mcloud through the controller, which checks the
  caller with a SubjectAccessReview. RBAC stays the one place that says
  who owns an app.
- **trust-manager** copies OpenBao's private CA into every namespace
  (ConfigMap `openbao-ca`). A namespaced SecretStore can only read a CA
  from its own namespace.

**Rejected:**
- Sealed Secrets or per-namespace SOPS keys: they fix "editing needs the
  master key", but not access control, audit or the bootstrap order.
- A SaaS store (Bitwarden, 1Password, Doppler): tenants' secrets at a
  third party.
- OpenBao on the internet with per-user policies for app owners: a second
  copy of ownership that has to be kept in sync with the registry.
- Raft storage: it would need a snapshot job with its own token and S3
  key, while CNPG's backups, PITR, runbook and alerts already exist.
- A ClusterSecretStore for the platform: any namespace whose ExternalSecret
  names it could read it.

**Revisit when:** OpenBao needs to be highly available (a second replica
plus `ha_enabled` on Postgres), or apps want secrets other than
`app-secrets`, which would mean letting tenants write ExternalSecrets.

See also: [openbao-self-init-and-audit](openbao-self-init-and-audit.md),
[eso-takeover-of-existing-secrets](eso-takeover-of-existing-secrets.md),
[maxcloud-secrets-in-cluster](maxcloud-secrets-in-cluster.md),
[app-kustomizations-no-sops](app-kustomizations-no-sops.md).

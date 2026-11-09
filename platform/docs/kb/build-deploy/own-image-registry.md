---
title: maxcloud runs its own registry (zot) instead of GHCR
kind: decision
area: build-deploy
tags: [registry, zot, ghcr, oidc, github, maxcloud]
learned: 2026-11-06
updated: 2026-11-08
where:
  - infrastructure/controllers/registry/config.json
  - infra/storage/maxcloud.tf
---

# maxcloud runs its own registry (zot) instead of GHCR

**Decision:** apps' images go to zot at `registry.gronblom.dev`, stored in
the `maxcloud-apps` Object Storage (bucket `maxcloud-images`). Logins are
OIDC tokens, never passwords: Authelia's for people, GitHub Actions' for
pushes from CI, and ServiceAccount tokens for each app's pulls (below).
run-deploy has pushed only there since 2026-11-06; there's no GHCR
fallback.

**Why not GHCR:** with GHCR, the cluster pulls private images with
someone's classic PAT, and only automation that doesn't exist could avoid
that. Checked 2026-11-06:

- GHCR doesn't accept GitHub App installation tokens outside Actions:
  login succeeds, the pull is denied (community discussion 171423, Aug
  2025, still open in Sept 2026). Only classic PATs work.
- A package's visibility can't be changed through the API (the REST
  description has no such endpoint), and a new package is private even if
  its repo is public. So "make it public" is a manual step per app.
- With other users, one PAT can't read their packages at all.

**Why zot:** it validates OIDC tokens itself (`http.auth.bearer.oidc`,
with CEL claim mapping), so GitHub Actions and Authelia need no token
service of ours; built-in garbage collection and retention policies
(Distribution's GC needs the registry read-only); S3 storage through
Distribution's driver (aws-sdk-go v1, so no checksum trouble with UpCloud);
reloads access control and htpasswd without a restart; one small pod.

**Rejected:** Distribution (no OIDC, offline GC), Harbor (Postgres, Redis
and several pods: too much for the CPU budget).

**How zot behaves:**

- In bearer mode the token endpoint only exchanges OIDC JWTs and zot API
  keys, not htpasswd passwords, and clients follow the Bearer challenge. So
  kubelet pull secrets need a JWT as the password too (username anything
  without a colon, e.g. `zot`).
- `realm` must be zot's own absolute token URL
  (`https://registry.gronblom.dev/zot/auth/token`).
- Its metadata database (push and pull times, for retention) can live in
  an emptyDir: a start rebuilds it from storage and takes each image's push
  time from the manifest object's modification time (`pkg/meta/parse.go`).
  Pull times are lost, so retention rules here use push times only
  (`mostRecentlyPushedCount`, `pushedWithin`).
- The image runs as root by default; it runs fine as 65532 with the
  metadata directory writable.
- v2.1.23 accepts the signing algorithms from the issuer's discovery
  document (Authelia lists ML-DSA among others) and denies bearer tokens
  without a repository grant.
- `zot verify config.json` checks a config offline.
- **zot refuses Docker v2 manifests unless `http.compat: ["docker2s2"]`:**
  pack (buildpacks) pushes `application/vnd.docker.distribution.manifest.v2+json`,
  and without it the manifest PUT gets `415 MANIFEST_INVALID` after the
  layers uploaded fine. docker 29 pushes OCI manifests, so a test with
  `docker push` doesn't show it.
- **In the bucket, keys start with `zot/zot/<repo>/`:** the driver's
  `rootdirectory` (`/zot`) plus zot's own prefix. Harmless; changing it
  would strand what's stored.
- **Measured 2026-11-06:** a ~2 GB image (renovate, largest layer 589 MB
  uncompressed) pushed from the laptop in 18s, through multipart uploads.
- **Apps' pull credentials are ServiceAccount tokens** (audience
  `registry.gronblom.dev`) of `maxcloud-image-pull` in each app namespace,
  minted by the maxcloud controller. zot's second OIDC issuer is the
  cluster's own (`https://kubernetes.default.svc.cluster.local`, CA from
  the `kube-root-ca.crt` ConfigMap); the CEL mapping makes the namespace the
  username, and `${username}/**` grants read. To fetch the issuer's
  discovery document and keys zot logs in to nothing, and the API server
  answers 403 to anonymous requests until `system:unauthenticated` is bound
  to `system:service-account-issuer-discovery`.
- **The controller may mint tokens for one ServiceAccount name only:**
  creating a token names its ServiceAccount, so `resourceNames` limits
  `serviceaccounts/token` `create`. Without that, the controller could act
  as any ServiceAccount in the cluster.

See also: [traefik-read-timeout](../networking/traefik-read-timeout.md),
[buildpacks](buildpacks.md).

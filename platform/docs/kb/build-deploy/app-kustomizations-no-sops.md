---
title: App Kustomizations must not decrypt with the platform's age key
kind: gotcha
area: build-deploy
tags: [flux, sops, age, maxcloud, secrets, security]
learned: 2026-11-07
updated: 2026-11-07
where:
  - apps/maxcloud/resourceset.yaml
---

# App Kustomizations must not decrypt with the platform's age key

**Symptom:** none visible. Found while scoping Flux multi-tenancy
(`private/docs/research/flux-multitenant.md`).

**Cause:** the registry's per-app Kustomization had
`decryption: {provider: sops, secretRef: {name: sops-age}}`. `sops-age` is
the only age key, and it opens every platform secret (`private/secrets/`).
kustomize-controller (v1.10.1) decrypts a `secretGenerator`'s SOPS files
whole before building. The file isn't changed, so its MAC still matches.
An app repo with a copy of any homelab secret file under `secretGenerator`
gets the plaintext as a Secret in its namespace, where the app is admin.
Reproduced in kind. Neither the tenant admission policies nor Flux's
multitenant mode stop it.

**Fix:** no `decryption` on app Kustomizations. Apps keep secrets in the
cluster (`mcloud secrets`,
[maxcloud-secrets-in-cluster](maxcloud-secrets-in-cluster.md)). If an app
ever needs SOPS, it needs an age key of its own, never `sops-age`.

See also: [admin-role-aggregates-controller-crds](../ops/admin-role-aggregates-controller-crds.md).

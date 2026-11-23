---
title: maxcloud apps' secrets live in the cluster, not in git
kind: decision
area: build-deploy
tags: [maxcloud, secrets, sops, age, mcloud]
learned: 2026-11-06
updated: 2026-11-22
where:
  - infrastructure/controllers/maxcloud/kustomization.yaml
  - infrastructure/controllers/maxcloud/config.yaml
---

# maxcloud apps' secrets live in the cluster, not in git

**Since 2026-11-22** apps' secrets are in OpenBao at
`apps/<namespace>/app-secrets`. `mcloud secrets` writes them through the
API gateway, and ESO makes the Secret `app-secrets`
([openbao-secrets](openbao-secrets.md)). With `MAXCLOUD_OPENBAO_ADDR` set,
the controller makes no age copies and restores nothing; it copies each
existing `app-secrets` into OpenBao once. What follows is the earlier
design, which still applies to a controller without OpenBao, and why SOPS
in app repos was dropped.

**Decision (2026-11-06):** `mcloud secrets set` writes the Secret
`app-secrets` in the app's namespace, for GitOps and direct apps alike.
The maxcloud controller copies it to `maxcloud-registry/secrets/<app>.age`,
encrypted to `MAXCLOUD_SECRETS_AGE_KEY`, and recreates it when it's missing.
Code: maxcloud repo, `internal/cli/secrets.go` and
`controller/internal/controller/secrets.go`.

**Why not SOPS in the app's repo (the old way):** a GitOps app's
`deploy/k8s/secrets.sops.yaml` was encrypted to the homelab's own Flux age
key. Changing one value means decrypting the file, so every edit needed
that master key (it decrypts every homelab secret), the `sops` binary, and
GitHub access to the private homelab repo for the recipient. Only the owner
could do it, and values arrived only on the next pod start.

**Gotchas:**
- mcloud updates the Secret in place and never deletes it: the controller
  puts a missing one back from its copy, so a delete-and-recreate would
  race it. Deleting it by hand brings it back too.
- Moving an app off `secrets.sops.yaml` (`mcloud secrets migrate`) takes
  over the Secret Flux already decrypted (no key needed) and marks it
  `kustomize.toolkit.fluxcd.io/prune: disabled`, so Flux doesn't delete it
  when the file leaves git. Since 2026-11-07 Flux doesn't decrypt app
  repos at all ([app-kustomizations-no-sops](app-kustomizations-no-sops.md)),
  so the Secret already in the cluster is the only copy migrate can take
  over.
- A change restarts functions via a pod-template annotation applied with
  its own field manager (`mcloud-restart`), which Flux's apply leaves alone.
- The controller's Secret cache only holds Secrets labelled
  `app.kubernetes.io/managed-by=mcloud`; nothing else in it reads Secrets
  through the cache.

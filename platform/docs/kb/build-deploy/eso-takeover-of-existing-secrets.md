---
title: ESO taking over existing Secrets keeps Flux's labels, and Merge empties one whose path is missing
kind: gotcha
area: build-deploy
tags: [external-secrets, eso, flux, prune, secrets, openbao, migration]
learned: 2026-11-22
updated: 2026-11-26
where:
  - secrets/
  - apps/maxcloud/resourceset.yaml
  - scripts/sops-to-bao.sh
---

# ESO taking over existing Secrets keeps Flux's labels, and Merge empties one whose path is missing

Found in kind with ESO 2.15.0, while planning the move from SOPS
([openbao-secrets](openbao-secrets.md)).

- **Takeover works, labels stay.** An ExternalSecret with
  `creationPolicy: Owner` adopts a Secret that already exists. It replaces
  the data entirely (keys missing in OpenBao go) and sets itself as owner,
  but it keeps the other labels, `kustomize.toolkit.fluxcd.io/name`
  included. When the old Kustomization stops listing that Secret, Flux
  prunes it, and ESO makes it again a moment later. Hence
  `scripts/sops-to-bao.sh` marks each Secret
  `kustomize.toolkit.fluxcd.io/prune=disabled` before the switch.
- **A path that doesn't exist: an error with `Retain`, an empty Secret
  with `Merge`.** Either way the ExternalSecret reports
  `could not get secret data from provider` (event
  `MissingProviderSecret`). With `Retain` the Secret keeps its values;
  with `Merge` ESO removes them (event `Updated`). This cost data on
  2026-11-26: apps' ExternalSecrets (`Merge`) were created 15s before the
  controller copied each existing `app-secrets` into OpenBao, ESO emptied
  the three that had values, and the import then copied the empty
  Secrets. They came back from the old controller's age copies
  (`maxcloud-registry/secrets/<app>.age`), which were still intact:
  decrypt each with `MAXCLOUD_SECRETS_AGE_KEY`, `bao kv put` it into
  `apps/<app>/app-secrets`, and force-sync the ExternalSecret. So the
  path must exist before an ExternalSecret with `Merge` first takes over a
  Secret that has values. An empty KV v2 secret (`{}`) syncs fine, so the
  maxcloud controller writes one for each new app.
- **`deletionPolicy: Retain` never empties a Secret.** When the data in
  OpenBao becomes empty (the last `mcloud secrets unset`), ESO reports
  Ready but leaves the old values. Apps' ExternalSecret uses `Merge`. The
  platform's keep `Retain`: there, losing a path should leave the last
  values in place rather than empty them under a running controller.
- **`force-sync` is fast.** Setting the annotation
  `force-sync=<anything new>` re-syncs within half a second, and
  `status.syncedResourceVersion` changes once it has.
- **A namespaced SecretStore can't name another namespace's
  ServiceAccount.** ESO's webhook refuses it: `namespace should either be
  empty or match the namespace of the SecretStore for a namespaced
  SecretStore`.

**Recognise it:** `kubectl get externalsecret -A` (the STATUS column), and
`kubectl -n <ns> get secret <name> -o jsonpath='{.metadata.labels}'`.

See also: [openbao-secrets](openbao-secrets.md).

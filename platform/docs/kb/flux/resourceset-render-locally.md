---
title: Render the app registry locally with flux-operator build rset
kind: fact
area: flux
tags: [flux-operator, resourceset, maxcloud]
learned: 2026-11-04
updated: 2026-11-04
where:
  - apps/maxcloud/resourceset.yaml
---

# Render the app registry locally with flux-operator build rset

```sh
mise x aqua:controlplaneio-fluxcd/flux-operator@0.62.1 -- \
  flux-operator build rset -f apps/maxcloud/resourceset.yaml \
  --inputs-from-provider apps/maxcloud/apps/run-demo.yaml
```

- The provider file must carry the label the ResourceSet selects
  (`run.gronblom.dev/app: "true"`), or the build fails with
  `map has no entry for key "name"`.
- Objects with `fluxcd.controlplane.io/reconcile: disabled` are left out of
  the output, as the operator leaves them out of the cluster: that's how the
  registry skips a GitRepository or Kustomization per app.
- Removing a provider garbage-collects everything generated for it (about
  12 s in a kind test), namespace and data included.

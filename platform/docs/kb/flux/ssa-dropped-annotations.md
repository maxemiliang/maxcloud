---
title: Removing Flux's last annotation on an object can wipe the others
kind: gotcha
area: flux
tags: [flux, server-side-apply, knative, migration]
learned: 2026-11-04
updated: 2026-11-04
where:
  - apps/maxcloud/resourceset.yaml
---

# Removing Flux's last annotation on an object can wipe the others

**Symptom:** after moving run-demo to the app registry, its Kustomization
failed: `admission webhook "validation.webhook.serving.knative.dev" denied
the request: validation failed: annotation value is immutable:
metadata.annotations.serving.knative.dev/creator`.

**Cause:** the move added `kustomize.toolkit.fluxcd.io/prune: disabled`
temporarily, then dropped it. On `whoami` and `webhook` it was the only
annotation the kustomize-controller field manager owned. Server-side apply
without it dropped the whole annotations map, including Knative's
`creator`/`lastModifier`, which no manager owns. `hello` was fine: it kept an
annotation of its own (`cloudflare-proxied`).

**Fix:** remove the annotation with a plain patch first, which leaves the
others alone:
`kubectl -n run-demo annotate ksvc whoami webhook kustomize.toolkit.fluxcd.io/prune-`.

**Recognise it:** a server-side dry run of the manifest fails the same way,
and succeeds with the old annotation kept.

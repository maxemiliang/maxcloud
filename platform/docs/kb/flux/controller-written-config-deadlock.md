---
title: A config only a controller writes can deadlock Flux's ordering
kind: gotcha
area: flux
tags: [flux, dependsOn, wait, registry, zot, maxcloud]
learned: 2026-11-06
updated: 2026-11-06
where:
  - infrastructure/controllers/registry/kustomization.yaml
  - clusters/personal/maxcloud.yaml
---

# A config only a controller writes can deadlock Flux's ordering

**Symptom:** after switching zot to the ConfigMap `zot-config` that the
maxcloud controller writes, zot's pod stayed `ContainerCreating` (the
ConfigMap didn't exist), `infra-controllers` stayed "Reconciliation in
progress", and the maxcloud controller never got its new version.

**Cause:** `infra-controllers` waits for its workloads to be healthy, zot
among them; the Kustomization that deploys the maxcloud controller
`dependsOn` `infra-controllers`. zot needed what only the new controller
writes. The same happens on a new cluster.

**Fix:** Flux seeds the ConfigMap once from the same base (a
configMapGenerator entry with the annotation
`kustomize.toolkit.fluxcd.io/ssa: IfNotPresent`): zot can start, and Flux
never updates it after creating it, so the controller owns it from then on.

**Rule:** anything a workload in `infra-controllers` needs at start must
exist without the maxcloud controller.

See also: [kstatus-cr-without-conditions](kstatus-cr-without-conditions.md).

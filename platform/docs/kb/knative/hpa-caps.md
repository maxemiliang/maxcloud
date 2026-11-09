---
title: Knative's activator and webhook HPAs need caps on a small cluster
kind: gotcha
area: knative
tags: [knative, hpa, resources]
learned: 2026-11-04
updated: 2026-11-04
where:
  - infrastructure/configs/knative/serving.yaml
  - scripts/crd-schemas.sh
---

# Knative's activator and webhook HPAs need caps on a small cluster

**Symptom:** with trimmed CPU requests, Knative's own HPAs would scale the
activator and webhook out at the first burst (up to 21 and 5 pods), since
they scale on CPU relative to the request. That eats the CPU budget a drain
needs.

**Fix:** cap `maxReplicas` with merge patches in the KnativeServing's
`spec.patches`: activator 3, webhook 2. The operator owns the HPAs, so
editing them directly is undone.

**Also:** the upstream PDBs (minAvailable 80%) allow no evictions with one or
two replicas and block every drain; the KnativeServing sets
`podDisruptionBudgets` to maxUnavailable 1.

The datreeio CRD catalog's KnativeServing schema lacks `patches`, so
`scripts/crd-schemas.sh` generates a local schema
(`scripts/schemas/operator.knative.dev/`), which `validate.sh` checks first.

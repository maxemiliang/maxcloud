---
title: An admission policy's params are fetched before its matchConditions
kind: gotcha
area: ops
tags: [admission, validatingadmissionpolicy, cel, maxcloud, knative, custom-domains]
learned: 2026-11-06
updated: 2026-11-06
where:
  - infrastructure/configs/run/route-admission.yaml
  - infrastructure/configs/run/registry-admission.yaml
  - infrastructure/configs/run/domains.yaml
---

# An admission policy's params are fetched before its matchConditions

**Symptom:** with the ConfigMap `maxcloud-system/maxcloud-domains` gone,
every HTTPRoute write in an app namespace fails, Knative's controller's
too (so no service deploys), and so does every registry entry write, with:

```
denied request: failed to configure binding: no params found for policy binding with `Deny` parameterNotFoundAction
```

**Cause:** the API server resolves a binding's `paramRef` before it
evaluates the policy's `matchConditions`. An exemption in
`matchConditions` (Knative's controller, entries without `domains`)
doesn't help when the params are missing. Found in a kind test,
2026-11-06.

**Fix:** none needed; it's deliberate. Both policies use
`parameterNotFoundAction: Deny`, so they fail closed. The ConfigMap lives in
git next to the policies (`infrastructure/configs/run`), and Flux puts it
back. `Allow` would fail open: without the ConfigMap, every app could
route any hostname.

**Recognise it:** `kubectl -n maxcloud-system get configmap maxcloud-domains`.

See also: [custom-domains-copy-route](../knative/custom-domains-copy-route.md),
[admin-role-aggregates-controller-crds](admin-role-aggregates-controller-crds.md).

---
title: kubectl get buckets means Flux's Bucket, not maxcloud's
kind: gotcha
area: flux
tags: [flux, maxcloud, kubectl, crd, buckets]
learned: 2026-11-05
updated: 2026-11-05
where:
  - clusters/personal/maxcloud.yaml
---

# kubectl get buckets means Flux's Bucket, not maxcloud's

**Symptom:** `kubectl -n run-demo get bucket smoke` answers
`Error from server (NotFound): buckets.source.toolkit.fluxcd.io "smoke" not found`
when the app does have a bucket.

**Cause:** two CRDs share the plural `buckets`: Flux's source kind
(`buckets.source.toolkit.fluxcd.io`) and the maxcloud controller's
(`buckets.maxcloud.gronblom.dev`). kubectl picks Flux's.

**Fix:** name the group, or use the maxcloud category:

```fish
kubectl get maxcloud -A
kubectl -n run-demo get buckets.maxcloud.gronblom.dev smoke -o yaml
```

Manifests are fine: they say `apiVersion: maxcloud.gronblom.dev/v1alpha1`.

**Recognise it:** an error that names `source.toolkit.fluxcd.io`.

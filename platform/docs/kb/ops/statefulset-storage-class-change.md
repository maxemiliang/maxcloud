---
title: A StatefulSet's storage class can't be changed; delete the StatefulSet and its PVC
kind: gotcha
area: ops
tags: [statefulset, pvc, storageclass, helm, flux, loki, prometheus]
learned: 2026-11-12
updated: 2026-11-12
where:
  - infrastructure/controllers/monitoring/loki.yaml
  - infrastructure/controllers/monitoring/kube-prometheus-stack.yaml
---

# A StatefulSet's storage class can't be changed; delete the StatefulSet and its PVC

**Symptom:** after changing a chart's `storageClass` (or the volume size)
in git, the HelmRelease's upgrade fails with
`StatefulSet.apps "loki" is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas', 'ordinals', 'template', 'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and 'minReadySeconds' are forbidden`,
and `infra-controllers` stays not ready.

**Cause:** a StatefulSet's `volumeClaimTemplates` are immutable, and the
existing PVC keeps its class anyway. Helm can't recreate the StatefulSet
by itself.

**Fix:** delete the StatefulSet and its PVC, and let Flux recreate both.
For Loki this only loses the write-ahead log. Its chunks and index live
in the `loki` bucket, so flush them first. In fish, before pushing:

```fish
# Upload what's only in the WAL (POST /flush returns at once, the flush takes a few seconds)
kubectl -n monitoring port-forward pod/loki-0 3100:3100 &
curl -fsS -X POST http://localhost:3100/flush; sleep 15; kill %1

flux suspend helmrelease loki -n monitoring
kubectl -n monitoring delete statefulset loki
kubectl -n monitoring delete pvc storage-loki-0
```

Then push. Once Flux has applied the new HelmRelease spec, resume it:

```fish
kubectl -n monitoring get helmrelease loki -o jsonpath='{.spec.values.singleBinary.persistence.storageClass}'
flux resume helmrelease loki -n monitoring
```

Resuming before the new spec is in reinstalls the old class. If the
upgrade already failed and used up its retries, use
`flux reconcile helmrelease loki -n monitoring --force` after the delete.

Prometheus (`prometheus-kube-prometheus-stack-prometheus`) works the
same way, but its PVC holds every metric, so it starts empty.

**Recognise it:** `flux get helmreleases -n monitoring` shows the
`Forbidden: updates to statefulset spec` message.

See also: [Block storage: MaxIOPS for databases, Standard for the rest](../data/block-storage-tiers.md)

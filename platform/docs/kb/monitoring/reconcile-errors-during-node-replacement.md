---
title: MaxcloudReconcileErrors fires during a node group replacement and clears by itself
kind: gotcha
area: monitoring
tags: [alerts, maxcloud, controller, cnpg, webhook, node-group, upgrade]
learned: 2026-11-21
updated: 2026-11-21
where:
  - infrastructure/configs/monitoring/maxcloud.yaml
  - infra/cluster/nodes.tf
---

# MaxcloudReconcileErrors fires during a node group replacement and clears by itself

**Symptom:** `MaxcloudReconcileErrors` for `database` fires during or just
after a node group replacement (a plan change, or a UKS upgrade replacing
nodes). The controller's log has errors like:

- `failed calling webhook "mcluster.cnpg.io": failed to call webhook: Post
  "https://cnpg-webhook-service.cnpg-system.svc:443/...": context deadline exceeded`
  (also `vcluster`, `mscheduledbackup`, `vscheduledbackup`);
- `dial tcp 10.96.x.x:9090: connect: operation not permitted` from
  `appapi`, right after Prometheus restarts on a new node;
- now and then an UpCloud API 500 (`ERROR_GENERAL_FAILURE`).

**Cause:** while pods move from the old nodes to the new ones, the CNPG
operator (which serves the webhook) and Prometheus restart, and requests
to them fail until they're up again. On 2026-11-21 the errors lasted from
about 18:02 to 18:24 UTC, close to the whole 25-minute replacement. The
alert fires on any error in the last 15 minutes, held for 30 minutes, so a
replacement that long sets it off.

**Fix:** nothing, unless it outlasts the replacement. It clears about 15
minutes after the last error.

**Recognise it:** the errors stop once the old nodes are gone, and
everything is Ready again:

```fish
kubectl get maxcloud -A
kubectl -n maxcloud-system logs deploy/maxcloud-controller --since=10m | grep -c '"level":"error"'
```

If the errors go on after the replacement, it's something else: follow
the alert's own checks.

See also: [node-plans-and-cost](../ops/node-plans-and-cost.md).

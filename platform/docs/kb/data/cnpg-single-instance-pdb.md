---
title: A single-instance CNPG cluster's PDB blocks every node drain
kind: gotcha
area: data
tags: [cnpg, postgres, pdb, drain, kured, node-group]
learned: 2026-11-21
updated: 2026-11-21
where:
  - apps/run-demo/queue.yaml
  - ../controller/internal/controller/cnpg.go
---

# A single-instance CNPG cluster's PDB blocks every node drain

**Symptom:** a node stays `Ready,SchedulingDisabled` and its drain never
ends. On 2026-11-21 the old `default` group's last node wouldn't go during
a node group replacement. The one pod left on it (DaemonSets aside) was
`run-demo/queue-db-1`, and `kubectl get pdb -A` showed
`queue-db-primary` with `ALLOWED DISRUPTIONS 0`.

**Cause:** CNPG makes a `<cluster>-primary` PDB with `minAvailable: 1` by
default (`enablePDB: true`). With two instances it switches the primary
over and the drain goes on. With one instance there's nothing to switch
to, so the eviction is refused forever.

**Fix:** `enablePDB: false` on every single-instance Cluster. The maxcloud
controller already does this for its databases (`cnpg.go`); hand-written
Clusters such as `run-demo`'s `queue-db` need it set. The pod then restarts
on another node and its disk follows it, with a short outage.

**Recognise it:**

```fish
kubectl get pdb -A | awk 'NR==1 || $5 == "0"'
```

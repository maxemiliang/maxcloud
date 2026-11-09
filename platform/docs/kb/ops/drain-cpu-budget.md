---
title: Everything must fit on one node while the other drains
kind: fact
area: ops
tags: [capacity, cpu, drain, kured]
learned: 2026-11-04
updated: 2026-11-05
where:
  - infra/cluster/nodes.tf
  - clusters/personal/flux-system/flux-instance.yaml
---

# Everything must fit on one node while the other drains

- Two nodes, STARTER-2xCPU-16GB, about 15.5 GiB allocatable each. Memory
  is plentiful; CPU requests are the limit.
- During a drain (kured reboots 01:00-04:00 UTC, node upgrades) one node
  holds everything. Requested on one node: about 1.74 of 2.0 cores
  (2026-11-04, with the first apps), then about 1.44 after Flux's
  requests were trimmed (2026-11-05). Sum both nodes' requests and count
  each DaemonSet pod once (alloy, node-exporter, traefik, kured: ~95m per
  node).
- Flux asked for 100m CPU per controller (four pods) but used 2-4m. Its
  requests are now 25m (FluxInstance patches and the operator's Helm
  values), with the limits unchanged, which freed 300m.
- App pods run at PriorityClass `tenant` (-100, never preempting): when it
  doesn't fit, they're the ones left Pending, not the platform.
- Trim requests before adding components; check with
  `kubectl describe node | grep -A8 'Allocated resources'`.

See also: [node-autoscaling-burst-pool](node-autoscaling-burst-pool.md),
the plan for when this budget runs out.

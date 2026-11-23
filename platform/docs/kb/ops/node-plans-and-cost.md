---
title: The default nodes are 8 GB and the sandbox node is off unless sessions are wanted, to keep the bill near €51
kind: decision
area: ops
tags: [cost, capacity, node-group, upcloud, sandbox, plan]
learned: 2026-11-21
updated: 2026-11-21
where:
  - infra/cluster/nodes.tf
  - infra/cluster/variables.tf
---

# The default nodes are 8 GB and the sandbox node is off unless sessions are wanted

**Decision (2026-11-21):** cut the bill from about €75 to about €51 a month.

- `default` goes back to 2× STARTER-2xCPU-8GB (~€18 each), from 2×
  STARTER-2xCPU-16GB (~€24), which it had been on since 2026-11-01.
- The `sandbox` node group (STARTER-2xCPU-4GB, ~€12 a month if left on)
  exists only while `tofu apply -var sandbox_nodes=1` says so (`count` on
  the resource). How: [sandbox-sessions runbook](../../runbooks/sandbox-sessions.md).

**Why:**

- One person uses the platform, and agent sessions are rare. An idle
  sandbox node was 16% of the bill, and nodes are billed by the hour.
- On one node the limit is CPU requests (2 cores), not memory
  ([drain-cpu-budget](drain-cpu-budget.md)), so most of the 16 GB sat
  unused. On 2026-11-01 one node ran everything in about 5.5 GiB, against
  about 7.6 GiB allocatable on an 8 GB node. Measured again 2026-11-21
  before the move (`kubectl top pods`): the pods on both default nodes
  used about 5.7 GiB, so about 4.9 GiB on one node with each DaemonSet
  counted once; memory requests were about 5.3 GiB. It fits, with less
  headroom than CPU.
- The plan change replaces `default` under `create_before_destroy`: the
  new group comes up before the old one is deleted. The old group's last node
  hung on `run-demo`'s single-instance database
  ([cnpg-single-instance-pdb](../data/cnpg-single-instance-pdb.md)), and
  `MaxcloudReconcileErrors` fired while pods moved
  ([reconcile-errors-during-node-replacement](../monitoring/reconcile-errors-during-node-replacement.md)).

**Gotcha: a node group can't be scaled to 0.** The provider (v5.48.0)
accepts `node_count = 0`, but the PATCH it sends is `{}`: UpCloud's Go
client drops a zero `count`. The API answers 200 with `"count": 1`, and
tofu fails with `produced an unexpected new value: .node_count: was
cty.NumberIntVal(0), but now cty.NumberIntVal(1)`. `upctl kubernetes
nodegroup scale --count 0` sends the same `{}` (see `upctl --debug`). So
the group is created and deleted instead of scaled.

**Rejected:**

- **One default node (~€39 in all):** every kured reboot, UKS upgrade and
  host failure would take everything down.
- **Merging the two Object Storage instances (−€5):** the maxcloud
  controller's sub-account would no longer be scoped to the apps' instance.
- **Standard instead of MaxIOPS block storage (about −€2):** see
  [block-storage-tiers](../data/block-storage-tiers.md).

**Revisit when:**

- memory on one node nears 7.6 GiB during a drain, or pods get evicted
  for memory: go back to 16 GB (+€12), or to STARTER-4xCPU-16GB (about
  €28 a node) if CPU is short too
  ([node-autoscaling-burst-pool](node-autoscaling-burst-pool.md));
- agent sessions become a daily thing: leaving the node on costs about
  €12 a month.

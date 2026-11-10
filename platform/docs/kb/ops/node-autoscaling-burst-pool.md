---
title: Node autoscaling, when we add it, is a tainted burst node group for app pods
kind: decision
area: ops
tags: [capacity, autoscaling, cluster-autoscaler, upcloud, node-group, cost, kured]
learned: 2026-11-04
updated: 2026-11-05
where:
  - infra/cluster/nodes.tf
  - infrastructure/configs/run/priorityclass.yaml
  - infrastructure/configs/run/admission.yaml
---

# Node autoscaling, when we add it, is a tainted burst node group for app pods

**Decision:** not yet. As of 2026-11-04 the cluster stays at 2×
STARTER-2xCPU-16GB with no autoscaler. When we add autoscaling, it scales a
separate `burst` node group that only app pods use. `default` stays fixed at
2 nodes.

**The plan:**

- **`burst` node group** in `infra/cluster/nodes.tf`:
  - `name_prefix = "burst"`, plan STARTER-2xCPU-4GB, `node_count = 1`.
  - Label `pool=burst`, taint `homelab/pool=burst:NoSchedule`.
  - `lifecycle { ignore_changes = [node_count], create_before_destroy = true }`.
  - No anti-affinity.
  - `default` needs no `ignore_changes`; the autoscaler never resizes it.
- **Getting app pods onto it:** a MutatingAdmissionPolicy (served at v1 on
  1.36) beside `run-tenant-priority`. It adds the toleration and a
  *required* node affinity for `pool=burst` to pods in namespaces labelled
  `run.gronblom.dev/tenant`. App repos and Knative flags stay unchanged.
- **DaemonSets on burst nodes:**
  - Cilium, CSI, konnectivity and node-exporter already tolerate every taint.
  - Alloy needs the toleration added, or app logs are lost.
  - Traefik stays off burst nodes. With `externalTrafficPolicy: Local`, those
    nodes fail the LB health check, so removing one drops no traffic. Check
    how UpCloud's LB treats those members.
- **Cluster autoscaler:**
  - Built from UpCloud's fork; we build the image ourselves.
  - Installed as a Flux HelmRelease of the upstream `cluster-autoscaler`
    chart with `cloudProvider: upcloud`.
  - Gets its own Flux Kustomization that nothing depends on. Under
    `infra-controllers` a crash-looping autoscaler (say, a stale cluster ID
    after a DR rebuild) would block configs and apps.
  - Flags:
    - `--nodes=2:2:<default group>`, `--nodes=1:5:<burst group>`,
      `--max-nodes-total=7`
    - `--expendable-pods-priority-cutoff=-100`
    - `--skip-nodes-with-local-storage=false`,
      `--skip-nodes-with-system-pods=false`
    - `--scale-down-unneeded-time=30m`, `--scale-down-delay-after-add=30m`
- **Credentials:**
  - Sub-account `homelab_autoscaler` (no dashes allowed, see [upcloud-sub-account-names](upcloud-sub-account-names.md)) with `managed_kubernetes` on this
    cluster's UUID only.
  - A token restricted to the NAT egress IP; 365 days is the maximum expiry.
  - A SOPS secret with `UPCLOUD_TOKEN` and `UPCLOUD_CLUSTER_ID`.
- **Alerts:** the autoscaler is down or stalled (`last_activity`),
  `cluster_safe_to_autoscale == 0`, scale-ups fail, it's at max with pods
  still unschedulable, `node_group_min_count` is below what we set, and the
  node count is too high. Base that last one on `kube_node_info`, not on the
  autoscaler's own metrics.
- **Test:** a `pause` Deployment at priority `tenant` requesting more CPU
  than any current node has free.
  1. Run it once without the cutoff flag: it must stay Pending.
  2. With the flag, it must scale up.
  3. `tofu plan` while at the higher count must show no changes.
  4. Delete it, and measure the scale-down with a curl loop running.
- **Cost:** about €70/month at the minimum (€58 plus one burst node), about
  €12 for each extra burst node, €118 at 5 burst nodes. Prices are from
  [Spare Cores](https://keeper.sparecores.net/server_prices?vendor=upcloud&regions=fi-hel2)
  (third party, USD), converted at our €24 to their $27.04 for
  STARTER-2xCPU-16GB; check them in the Hub. Billing is hourly with a
  monthly cap.

**Why a burst pool rather than autoscaling `default`:**

- App pods stop using the platform's drain budget
  ([drain-cpu-budget](drain-cpu-budget.md)).
- kured drains of `default` no longer cause scale-ups.
- If the autoscaler misbehaves, only app pods are affected.

**Traps found while designing it (2026-11-04):**

- **No upstream provider.** `kubernetes/autoscaler` has no UpCloud provider.
  It lives only in [UpCloudLtd/autoscaler](https://github.com/UpCloudLtd/autoscaler),
  branch `feat/cluster-autoscaler-cloudprovider-upcloud`. The upstream
  [PR #10234](https://github.com/kubernetes/autoscaler/pull/10234) is open
  and unreviewed.
- **No image for 1.36.** UpCloud's images stop at
  `ghcr.io/upcloudltd/autoscaler:v1.29.5`. Build either the fork's latest
  commit as-is or the provider added onto `cluster-autoscaler-1.36.1`.
  Token auth (`UPCLOUD_TOKEN`) is in the code but in no published image.
- **App pods never trigger a scale-up by default.** The default
  `--expendable-pods-priority-cutoff` is -10, and `tenant` is -100.
- **Groups without `--nodes` get min 1 and max = the plan's `max_nodes`**
  (dev-md: 30). The autoscaler would take `default` down to one node: about
  1.8 cores are requested and one node has 2.0.
- **`name_prefix` gives every replacement a new group name**, which
  leaves `--nodes` stale. Before replacing a node group or upgrading
  Kubernetes:
  1. Suspend the HelmRelease and scale the autoscaler to 0.
  2. Apply.
  3. Update the names in the flags.
  4. Resume.
- **No scale from zero.** The fork's `TemplateNodeInfo` isn't implemented,
  so `burst` always keeps one node.
- **Scale-down blocks:** `skip-nodes-with-local-storage=true` (the default)
  pins nearly every node, because Flux, Grafana and CNPG use emptyDir. An
  app's CNPG primary (its PDB allows 0 disruptions) pins its burst node.
- **The token has no scopes.** It could scale to 30 nodes or delete the
  cluster.
- **UKS has no managed autoscaling.** The node-group API only takes `count`.

**Rejected or deferred:**

- **Autoscaling `default`:** it would react to every kured drain, and the
  min-1 risk would hit the platform.
- **UpCloud's Terraform example** (in the fork, `examples/terraform`): raw
  `kubernetes_deployment`, image v1.29.5, password auth, no `--nodes`.
- **Karpenter:** only community providers, alpha and beta.
- **`default` on STARTER-4xCPU-16GB:** about €28 per node instead of €24, so
  about €8/month in total, and it doubles the drain budget to 4 cores. This
  is the cheapest way to get headroom; not done yet as of 2026-11-04.

**Revisit when:** app pods stay Pending (`KnativeRevisionPodsPending`), the
drain budget runs out, or PR #10234 is merged (then use the official image
instead of building our own). Before building this, compare it with the
4-core plan.

See also: [drain-cpu-budget](drain-cpu-budget.md),
[sandbox-gvisor-on-uks](sandbox-gvisor-on-uks.md) (a second, tainted node
group exists since 2026-11-10: `sandbox`, fixed at one node).

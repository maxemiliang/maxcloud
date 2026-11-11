resource "upcloud_kubernetes_node_group" "default" {
  cluster = upcloud_kubernetes_cluster.this.id

  # The provider appends "-" plus 5 random chars (e.g. "default-x7k2p"), so a
  # replacement group can exist next to the old one; see create_before_destroy.
  name_prefix = "default"

  # 2 vCPU / 16 GB / 50 GB standard disk, ~€24/mo each. With two nodes, one node
  # must be able to run everything while the other is drained (kured reboots,
  # node group replacement), so usable capacity is one node's allocatable memory
  # (~15 GiB).
  #
  # Fixed size, no autoscaler. The plan for one (a separate tainted burst
  # group for app pods; this group stays at 2) and its traps:
  # docs/kb/ops/node-autoscaling-burst-pool.md.
  plan       = "STARTER-2xCPU-16GB"
  node_count = 2

  # Best effort: put the two nodes on different physical hosts, so one host
  # failure doesn't take out both. Changing it replaces the node group.
  anti_affinity = true

  # Forwarded to the Kubernetes Node objects as node labels too.
  labels = local.labels

  # Disk encryption is inherited from the cluster (data-at-rest). No SSH keys:
  # the nodes have no public IP anyway; use `kubectl debug node/...` instead.

  lifecycle {
    # Almost every attribute (plan, anti_affinity, taints, kubelet args, ...)
    # forces a new node group. Without this, tofu would delete both nodes first
    # and every pod would be down until the new group was ready. With it, the new
    # group comes up first, then the old one is deleted and its pods reschedule.
    create_before_destroy = true
  }
}

# A node of its own for sandboxed agent sessions (untrusted code, so never
# on the nodes that run Flux and the platform). Nothing schedules here
# unless it tolerates the taint, which the `gvisor` RuntimeClass adds for the
# pods that use it. What runs on it: docs/runbooks/sandbox-sessions.md.
resource "upcloud_kubernetes_node_group" "sandbox" {
  cluster = upcloud_kubernetes_cluster.this.id

  name_prefix = "sandbox"

  # 2 vCPU / 4 GB / 30 GB standard disk. An agent session starts at about
  # 1 CPU and 1 GiB, so two or three fit.
  plan       = "STARTER-2xCPU-4GB"
  node_count = 1

  # No anti_affinity: one node, and sessions are disposable.

  # Node labels too: `pool=sandbox` is what the gVisor installer and the
  # RuntimeClass select on.
  labels = merge(local.labels, { pool = "sandbox" })

  # Same key as the burst pool planned in
  # docs/kb/ops/node-autoscaling-burst-pool.md. Cilium, CSI and node-exporter
  # tolerate every taint; Traefik, Alloy and kured don't, so they stay off.
  taint {
    key    = "homelab/pool"
    value  = "sandbox"
    effect = "NoSchedule"
  }

  lifecycle {
    create_before_destroy = true
    # Once a cluster autoscaler resizes this group, add
    # `ignore_changes = [node_count]` here.
  }
}

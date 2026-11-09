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

resource "upcloud_kubernetes_cluster" "this" {
  name    = local.name
  zone    = var.zone
  network = upcloud_network.cluster.id
  labels  = local.labels

  # Single API server + single etcd member, no HA. Free.
  plan = "dev-md"

  version = var.kubernetes_version
  # When `version` is bumped, UKS upgrades the control plane and then replaces
  # nodes one at a time. With "manual" you'd have to recreate the node groups yourself.
  upgrade_strategy_type = "rolling-update"

  # Nodes only get private IPs; egress goes through the NAT gateway.
  private_node_groups = true

  # Default for node disks (node groups inherit it). Free, managed keys, roughly
  # 10-20% slower disk I/O. Persistent volumes are set per StorageClass instead.
  storage_encryption = "data-at-rest"

  # Who may reach the Kubernetes API. Doesn't affect the load balancer or nodes.
  # The NAT egress IP is included in case the nodes reach the API over its public
  # endpoint. UpCloud doesn't document this, and the IP is exclusively ours, so
  # including it is harmless.
  control_plane_ip_filter = concat(
    var.admin_cidrs,
    ["${one(upcloud_gateway.nat.address[*].address)}/32"],
  )

  # Private nodes need a working default route when they boot.
  depends_on = [upcloud_gateway.nat]
}

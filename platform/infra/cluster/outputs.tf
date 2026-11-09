output "zone" {
  value = var.zone
}

output "network_id" {
  value = upcloud_network.cluster.id
}

output "cluster_id" {
  description = "UKS cluster UUID; infra/flux uses it to fetch credentials through the ephemeral resource."
  value       = upcloud_kubernetes_cluster.this.id
}

output "node_group_name" {
  value = upcloud_kubernetes_node_group.default.name
}

output "control_plane_ip_filter" {
  value = upcloud_kubernetes_cluster.this.control_plane_ip_filter
}

output "nat_egress_ip" {
  description = "Public IP that all outbound traffic from the nodes appears to come from."
  value       = one(upcloud_gateway.nat.address[*].address)
}

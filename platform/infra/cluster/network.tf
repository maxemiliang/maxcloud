# Private SDN network for the cluster nodes. Nodes get no public IPs
# (private_node_groups), so all their outbound traffic goes:
#   node -> DHCP default route -> router -> NAT gateway -> internet
#
# Inbound traffic doesn't use this path at all: it arrives through the managed
# load balancer that the cluster creates later.

resource "upcloud_router" "this" {
  name   = "${local.name}-router"
  labels = local.labels

  # The NAT gateway adds a 0.0.0.0/0 "service" static route to this router.
  # The provider hides service routes, so no ignore_changes should be needed;
  # a clean second `tofu plan` after apply confirms it.
}

resource "upcloud_gateway" "nat" {
  name     = "${local.name}-nat"
  zone     = var.zone
  features = ["nat"]
  # The provider's default is "development", which is billed. Essentials is free:
  # single instance (no HA), 500 Mbit/s, 10k concurrent connections.
  plan   = "essentials"
  labels = local.labels

  router {
    id = upcloud_router.this.id
  }

  # Declaring an address block makes the provider record the public egress IP
  # in state (see the nat_egress_ip output).
  address {
    name = "egress"
  }
}

resource "upcloud_network" "cluster" {
  name   = "${local.name}-cluster"
  zone   = var.zone
  router = upcloud_router.this.id
  labels = local.labels

  ip_network {
    family  = "IPv4"
    address = var.network_cidr
    dhcp    = true
    # Hand out the router as the default gateway, so nodes route to the NAT.
    dhcp_default_route = true
  }
}

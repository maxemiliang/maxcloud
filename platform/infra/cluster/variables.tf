variable "state_passphrase" {
  description = "Passphrase for OpenTofu state encryption (min 16 chars). Set via TF_VAR_state_passphrase."
  type        = string
  sensitive   = true
  ephemeral   = true

  validation {
    condition     = length(var.state_passphrase) >= 16
    error_message = "The state passphrase must be at least 16 characters."
  }
}

variable "zone" {
  description = "UpCloud zone for the network and cluster. fi-hel2 is also the primary zone of the europe-1 object storage."
  type        = string
  default     = "fi-hel2"
}

variable "network_cidr" {
  description = "CIDR of the cluster's private network. Cannot change after the cluster is created."
  type        = string
  # Must not overlap UKS's reserved ranges: 172.31.240.0/24 (control plane),
  # 10.128.0.0/12 (services), 192.168.0.0/16 (pods), 10.33.128.0/22 (forwarder),
  # 100.64.0.0/10 (load balancers), 10.6.0.0/16 (fi-hel2 utility).
  default = "172.16.10.0/24"
}

variable "kubernetes_version" {
  description = "UKS version ID (`upctl kubernetes versions`). Keep the kubectl pin in mise.toml within one minor version of it."
  type        = string
  default     = "1.36"
}

variable "admin_cidrs" {
  description = "CIDRs allowed to reach the Kubernetes API, e.g. [\"203.0.113.7/32\"]. Set via TF_VAR_admin_cidrs in .env."
  type        = list(string)

  validation {
    condition = length(var.admin_cidrs) > 0 && alltrue([
      for c in var.admin_cidrs : can(cidrhost(c, 0)) && !startswith(c, "0.0.0.0/")
    ])
    error_message = "admin_cidrs must be non-empty valid CIDRs, and not 0.0.0.0/0."
  }
}

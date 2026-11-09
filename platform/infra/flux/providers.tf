# Read the cluster ID from infra/cluster's (encrypted) state instead of copying it.
data "terraform_remote_state" "cluster" {
  backend = "s3"
  config = {
    bucket = "tfstate"
    key    = "cluster/terraform.tfstate"
    region = "europe-1"

    endpoints = {
      s3 = "https://q6c58.upcloudobjects.com"
    }
    use_path_style = true

    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_s3_checksum            = true
  }
}

# Credentials from the system keyring (homelab_api sub-account token).
provider "upcloud" {}

# Ephemeral: fetched from the UpCloud API on every plan/apply, handed to the
# providers below, and never written to state or plan files.
ephemeral "upcloud_kubernetes_cluster" "this" {
  id = data.terraform_remote_state.cluster.outputs.cluster_id
}

provider "kubernetes" {
  host                   = ephemeral.upcloud_kubernetes_cluster.this.host
  client_certificate     = ephemeral.upcloud_kubernetes_cluster.this.client_certificate
  client_key             = ephemeral.upcloud_kubernetes_cluster.this.client_key
  cluster_ca_certificate = ephemeral.upcloud_kubernetes_cluster.this.cluster_ca_certificate
}

provider "helm" {
  kubernetes = {
    host                   = ephemeral.upcloud_kubernetes_cluster.this.host
    client_certificate     = ephemeral.upcloud_kubernetes_cluster.this.client_certificate
    client_key             = ephemeral.upcloud_kubernetes_cluster.this.client_key
    cluster_ca_certificate = ephemeral.upcloud_kubernetes_cluster.this.cluster_ca_certificate
  }
}

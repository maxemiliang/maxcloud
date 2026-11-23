# OpenBao's bootstrap Secrets: the two things OpenBao needs before it can
# hand out anything (platform/secrets-store).
#
# - openbao-seal: the static seal key, 32 random bytes from
#   ~/.config/openbao/seal.key (make it with `openssl rand -out <path> 32`;
#   keep a copy in the password manager). Without it OpenBao's data and its
#   backups are unreadable.
# - openbao-backup-s3: the key for its database backups (infra/storage's
#   openbao-backups user).
#
# Both are write-only (binary_data_wo/data_wo): the state keeps only a
# revision number. Bump openbao_seal_key_revision to push a changed key file;
# the S3 key's revision follows the key itself.
#
# The namespace is made here because the Secrets must exist before Flux
# runs; platform/secrets-store/namespace.yaml adopts it, and tofu then
# leaves its labels and annotations alone.

data "terraform_remote_state" "storage" {
  backend = "s3"
  config = {
    bucket = "tfstate"
    key    = "storage/terraform.tfstate"
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

locals {
  openbao_backup_s3 = {
    AWS_ACCESS_KEY_ID     = data.terraform_remote_state.storage.outputs.openbao_backup_access_key_id
    AWS_SECRET_ACCESS_KEY = data.terraform_remote_state.storage.outputs.openbao_backup_secret_access_key
    AWS_REGION            = "europe-1"
  }
}

resource "kubernetes_namespace_v1" "openbao" {
  metadata {
    name = "openbao"
    labels = {
      "pod-security.kubernetes.io/enforce" = "restricted"
    }
  }

  lifecycle {
    ignore_changes = [metadata[0].labels, metadata[0].annotations]
  }
}

resource "kubernetes_secret_v1" "openbao_seal" {
  metadata {
    name      = "openbao-seal"
    namespace = kubernetes_namespace_v1.openbao.metadata[0].name
  }

  type = "Opaque"

  binary_data_wo = {
    key = filebase64(pathexpand(var.openbao_seal_key_path))
  }

  binary_data_wo_revision = var.openbao_seal_key_revision
}

resource "kubernetes_secret_v1" "openbao_backup_s3" {
  metadata {
    name      = "openbao-backup-s3"
    namespace = kubernetes_namespace_v1.openbao.metadata[0].name
  }

  type = "Opaque"

  data_wo = local.openbao_backup_s3

  data_wo_revision = parseint(substr(sha256(jsonencode(local.openbao_backup_s3)), 0, 12), 16)
}

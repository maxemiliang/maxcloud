terraform {
  required_version = "~> 1.13"

  required_providers {
    upcloud = {
      source  = "UpCloudLtd/upcloud"
      version = "5.48.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "3.3.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.4.0"
    }
  }

  backend "s3" {
    bucket = "tfstate"
    key    = "flux/terraform.tfstate"
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

    use_lockfile = true
  }

  encryption {
    key_provider "pbkdf2" "passphrase" {
      passphrase = var.state_passphrase
    }

    method "aes_gcm" "default" {
      keys = key_provider.pbkdf2.passphrase
    }

    state {
      method   = method.aes_gcm.default
      enforced = true
    }

    plan {
      method   = method.aes_gcm.default
      enforced = true
    }

    # Lets data.terraform_remote_state decrypt infra/cluster's state
    # (same passphrase).
    remote_state_data_sources {
      default {
        method = method.aes_gcm.default
      }
    }
  }
}

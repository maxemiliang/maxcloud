terraform {
  required_version = "~> 1.13"

  required_providers {
    upcloud = {
      source  = "UpCloudLtd/upcloud"
      version = "5.48.0"
    }
  }

  # State lives in the bucket created by infra/bootstrap.
  # Credentials: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (the tofu-state user).
  backend "s3" {
    bucket = "tfstate"
    key    = "cluster/terraform.tfstate"
    region = "europe-1"

    endpoints = {
      s3 = "https://q6c58.upcloudobjects.com"
    }
    use_path_style = true

    # Not AWS: skip the AWS-only lookups (STS, account ID, EC2 metadata,
    # region list) and the newer checksum headers.
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_s3_checksum            = true

    # Native S3 locking: writes cluster/terraform.tfstate.tflock with a
    # conditional PUT (If-None-Match: *) while an operation runs.
    use_lockfile = true
  }

  # Same scheme and passphrase as infra/bootstrap. Never rename these blocks
  # once state exists.
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
  }
}

# Credentials come from the system keyring (homelab-tofu sub-account token).
provider "upcloud" {}

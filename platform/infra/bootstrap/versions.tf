terraform {
  required_version = "~> 1.13"

  required_providers {
    upcloud = {
      source  = "UpCloudLtd/upcloud"
      version = "5.48.0"
    }
  }

  # No backend block: this stack uses local state (infra/bootstrap/terraform.tfstate)
  # because it creates the bucket that every other stack keeps its state in.
  #
  # The local state still contains a secret (the S3 secret key), so it is
  # encrypted. Never rename these key_provider/method blocks once state exists,
  # or OpenTofu can no longer decrypt it.
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

# Credentials come from the system keyring (`upctl account login`) or UPCLOUD_TOKEN.
provider "upcloud" {}

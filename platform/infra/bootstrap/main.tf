locals {
  labels = {
    managed-by = "opentofu"
    stack      = "bootstrap"
  }
}

# One Object Storage instance for the whole homelab: tofu state now, and
# backup / WAL buckets (those are added by infra/storage, not here).
resource "upcloud_managed_object_storage" "this" {
  name              = "homelab"
  region            = var.object_storage_region
  configured_status = "started"
  labels            = local.labels

  # Public endpoint, so it's reachable from the laptop and, via the NAT gateway,
  # from the private cluster nodes.
  network {
    family = "IPv4"
    name   = "public"
    type   = "public"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "upcloud_managed_object_storage_bucket" "tfstate" {
  service_uuid = upcloud_managed_object_storage.this.id
  name         = var.state_bucket_name

  # The UpCloud API deletes buckets even when they still contain objects.
  lifecycle {
    prevent_destroy = true
  }
}

# --- Least-privilege S3 user for the tofu s3 backend -------------------------
# Object Storage users are separate from UpCloud API accounts: they only get
# S3 access keys and IAM-style policies inside this one instance.

resource "upcloud_managed_object_storage_policy" "tfstate" {
  service_uuid = upcloud_managed_object_storage.this.id
  name         = "tfstate-readwrite"
  description  = "Read/write OpenTofu state in the ${var.state_bucket_name} bucket only."

  # The API wants the JSON policy URL-encoded.
  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListStateBucket"
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = "arn:aws:s3:::${var.state_bucket_name}"
      },
      {
        Sid      = "ReadWriteStateObjects"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Resource = "arn:aws:s3:::${var.state_bucket_name}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "tofu_state" {
  service_uuid = upcloud_managed_object_storage.this.id
  username     = "tofu-state"
}

resource "upcloud_managed_object_storage_user_policy" "tofu_state" {
  service_uuid = upcloud_managed_object_storage.this.id
  username     = upcloud_managed_object_storage_user.tofu_state.username
  name         = upcloud_managed_object_storage_policy.tfstate.name
}

resource "upcloud_managed_object_storage_user_access_key" "tofu_state" {
  service_uuid = upcloud_managed_object_storage.this.id
  username     = upcloud_managed_object_storage_user.tofu_state.username
  status       = "Active"
}

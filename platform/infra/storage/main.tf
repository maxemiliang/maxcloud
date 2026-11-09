# Storage that must outlive the cluster: backups live here, not in infra/cluster,
# so destroying and recreating the cluster (the DR drill) leaves them untouched.
# Buckets go into the Object Storage instance created by infra/bootstrap.

locals {
  labels = {
    managed-by = "opentofu"
    stack      = "storage"
  }
  object_storage_id = data.terraform_remote_state.bootstrap.outputs.object_storage_id
}

# infra/bootstrap keeps its state locally (it creates the state bucket itself).
data "terraform_remote_state" "bootstrap" {
  backend = "local"
  config = {
    path = "${path.root}/../bootstrap/terraform.tfstate"
  }
}

resource "upcloud_managed_object_storage_bucket" "postgres_backups" {
  service_uuid = local.object_storage_id
  name         = var.postgres_backup_bucket

  # The UpCloud API deletes buckets even when they still contain objects.
  lifecycle {
    prevent_destroy = true
  }
}

# --- S3 user for the Barman Cloud plugin -------------------------------------
# Can read, write and delete objects in the backup bucket only. Delete is needed:
# the plugin enforces the retention policy by removing old backups and WALs.

resource "upcloud_managed_object_storage_policy" "postgres_backups" {
  service_uuid = local.object_storage_id
  name         = "pg-backups-readwrite"
  description  = "Read/write CloudNativePG backups in the ${var.postgres_backup_bucket} bucket only."

  # The API wants the JSON policy URL-encoded.
  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListBackupBucket"
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:GetBucketLocation"]
        Resource = "arn:aws:s3:::${var.postgres_backup_bucket}"
      },
      {
        Sid    = "ReadWriteBackupObjects"
        Effect = "Allow"
        # AbortMultipartUpload: base backups are uploaded in parts, and an
        # interrupted upload must be cleaned up.
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"]
        Resource = "arn:aws:s3:::${var.postgres_backup_bucket}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "postgres_backups" {
  service_uuid = local.object_storage_id
  username     = "cnpg-backups"
}

resource "upcloud_managed_object_storage_user_policy" "postgres_backups" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.postgres_backups.username
  name         = upcloud_managed_object_storage_policy.postgres_backups.name
}

resource "upcloud_managed_object_storage_user_access_key" "postgres_backups" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.postgres_backups.username
  status       = "Active"
}

# --- Loki log storage ---------------------------------------------------------
# Logs outlive the cluster too: after losing it, its last logs are still here
# for the post-mortem. Loki deletes chunks older than its retention itself.

resource "upcloud_managed_object_storage_bucket" "loki" {
  service_uuid = local.object_storage_id
  name         = var.loki_bucket

  lifecycle {
    prevent_destroy = true
  }
}

resource "upcloud_managed_object_storage_policy" "loki" {
  service_uuid = local.object_storage_id
  name         = "loki-readwrite"
  description  = "Read/write Loki chunks and index in the ${var.loki_bucket} bucket only."

  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListLokiBucket"
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:GetBucketLocation"]
        Resource = "arn:aws:s3:::${var.loki_bucket}"
      },
      {
        Sid    = "ReadWriteLokiObjects"
        Effect = "Allow"
        # DeleteObject: the compactor enforces retention by deleting old chunks.
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"]
        Resource = "arn:aws:s3:::${var.loki_bucket}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "loki" {
  service_uuid = local.object_storage_id
  username     = "loki"
}

resource "upcloud_managed_object_storage_user_policy" "loki" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.loki.username
  name         = upcloud_managed_object_storage_policy.loki.name
}

resource "upcloud_managed_object_storage_user_access_key" "loki" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.loki.username
  status       = "Active"
}

# --- S3 user for OpenBao's database backups -----------------------------------
# OpenBao's storage is a CloudNativePG cluster (platform/secrets-store) that
# backs up to pg-backups/openbao/. A user of its own, limited to that prefix:
# infra/flux puts its key in the cluster as Secret openbao/openbao-backup-s3,
# because OpenBao can't hold the key to its own backups.

resource "upcloud_managed_object_storage_policy" "openbao_backups" {
  service_uuid = local.object_storage_id
  name         = "openbao-backups-readwrite"
  description  = "Read/write OpenBao's database backups under ${var.postgres_backup_bucket}/openbao/ only."

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
        Sid      = "ReadWriteOpenBaoBackups"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"]
        Resource = "arn:aws:s3:::${var.postgres_backup_bucket}/openbao/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "openbao_backups" {
  service_uuid = local.object_storage_id
  username     = "openbao-backups"
}

resource "upcloud_managed_object_storage_user_policy" "openbao_backups" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.openbao_backups.username
  name         = upcloud_managed_object_storage_policy.openbao_backups.name
}

resource "upcloud_managed_object_storage_user_access_key" "openbao_backups" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.openbao_backups.username
  status       = "Active"
}

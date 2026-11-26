# --- claude-history: the raw copy of Claude Code's transcripts ----------------
# Vector on the laptops writes every transcript line here as well as to
# ClickHouse (infrastructure/controllers/claude-history). It's the backup:
# ClickHouse is rebuilt from it (docs/runbooks/claude-history-restore.md).
# Nothing deletes from it; there's no lifecycle rule.
#
# Two S3 users, neither of which can delete:
# - claude-history-writer, on the laptops: may only put objects. A leaked
#   key can add objects but not read or remove what's there.
# - claude-history-reader, for a restore: may list and read. It has no key
#   until one is wanted: tofu apply -var claude_history_reader_key=true,
#   and without it again afterwards, which deletes the key.

resource "upcloud_managed_object_storage_bucket" "claude_history" {
  service_uuid = local.object_storage_id
  name         = var.claude_history_bucket

  lifecycle {
    prevent_destroy = true
  }
}

resource "upcloud_managed_object_storage_policy" "claude_history_writer" {
  service_uuid = local.object_storage_id
  name         = "claude-history-write-only"
  description  = "Put objects in the ${var.claude_history_bucket} bucket; no reads, lists or deletes."

  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "PutTranscripts"
        Effect = "Allow"
        # AbortMultipartUpload: a large batch may be uploaded in parts.
        Action   = ["s3:PutObject", "s3:AbortMultipartUpload"]
        Resource = "arn:aws:s3:::${var.claude_history_bucket}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "claude_history_writer" {
  service_uuid = local.object_storage_id
  username     = "claude-history-writer"
}

resource "upcloud_managed_object_storage_user_policy" "claude_history_writer" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.claude_history_writer.username
  name         = upcloud_managed_object_storage_policy.claude_history_writer.name
}

resource "upcloud_managed_object_storage_user_access_key" "claude_history_writer" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.claude_history_writer.username
  status       = "Active"
}

resource "upcloud_managed_object_storage_policy" "claude_history_reader" {
  service_uuid = local.object_storage_id
  name         = "claude-history-read-only"
  description  = "List and read the ${var.claude_history_bucket} bucket, for restoring ClickHouse."

  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListTranscripts"
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:GetBucketLocation"]
        Resource = "arn:aws:s3:::${var.claude_history_bucket}"
      },
      {
        Sid      = "ReadTranscripts"
        Effect   = "Allow"
        Action   = ["s3:GetObject"]
        Resource = "arn:aws:s3:::${var.claude_history_bucket}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "claude_history_reader" {
  service_uuid = local.object_storage_id
  username     = "claude-history-reader"
}

resource "upcloud_managed_object_storage_user_policy" "claude_history_reader" {
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.claude_history_reader.username
  name         = upcloud_managed_object_storage_policy.claude_history_reader.name
}

resource "upcloud_managed_object_storage_user_access_key" "claude_history_reader" {
  count        = var.claude_history_reader_key ? 1 : 0
  service_uuid = local.object_storage_id
  username     = upcloud_managed_object_storage_user.claude_history_reader.username
  status       = "Active"
}

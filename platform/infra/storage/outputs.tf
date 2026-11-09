output "s3_endpoint" {
  description = "S3 endpoint for the Barman Cloud plugin's ObjectStore and Loki."
  value       = data.terraform_remote_state.bootstrap.outputs.s3_endpoint
}

output "postgres_backup_bucket" {
  value = upcloud_managed_object_storage_bucket.postgres_backups.name
}

output "postgres_backup_access_key_id" {
  value = upcloud_managed_object_storage_user_access_key.postgres_backups.access_key_id
}

output "postgres_backup_secret_access_key" {
  value     = upcloud_managed_object_storage_user_access_key.postgres_backups.secret_access_key
  sensitive = true
}

output "loki_bucket" {
  value = upcloud_managed_object_storage_bucket.loki.name
}

output "loki_access_key_id" {
  value = upcloud_managed_object_storage_user_access_key.loki.access_key_id
}

output "loki_secret_access_key" {
  value     = upcloud_managed_object_storage_user_access_key.loki.secret_access_key
  sensitive = true
}

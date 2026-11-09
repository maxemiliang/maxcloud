output "s3_endpoint" {
  description = "Public S3 endpoint for the s3 backend in infra/cluster and infra/flux."
  value = one([
    for e in upcloud_managed_object_storage.this.endpoint : "https://${e.domain_name}"
    if e.type == "public"
  ])
}

output "object_storage_region" {
  value = upcloud_managed_object_storage.this.region
}

output "object_storage_id" {
  description = "Instance UUID; infra/storage adds backup buckets to it."
  value       = upcloud_managed_object_storage.this.id
}

output "state_bucket" {
  value = upcloud_managed_object_storage_bucket.tfstate.name
}

output "state_access_key_id" {
  value = upcloud_managed_object_storage_user_access_key.tofu_state.access_key_id
}

output "state_secret_access_key" {
  value     = upcloud_managed_object_storage_user_access_key.tofu_state.secret_access_key
  sensitive = true
}

# The maxcloud controller: `mcloud secrets` through the API gateway (after
# its own SubjectAccessReview of the caller), the one-time import of the old
# app-secrets, and cleanup when an app is deleted. Apps' mount only; the
# platform's secrets are out of its reach.
path "apps/data/*" {
  capabilities = ["create", "read", "update", "patch", "delete"]
}
path "apps/metadata/*" {
  capabilities = ["read", "list", "delete"]
}

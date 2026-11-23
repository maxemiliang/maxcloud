# Every namespace's ESO login (role `reader`, ServiceAccount eso-reader in
# any namespace) reads only its own namespace's path, in either mount. The
# namespace comes from the ServiceAccount token the API server signed, so a
# namespace can't name another. K8S_ACCESSOR is replaced with the Kubernetes
# auth mount's accessor by configure.sh.
path "platform/data/{{identity.entity.aliases.K8S_ACCESSOR.metadata.service_account_namespace}}/*" {
  capabilities = ["read"]
}
path "platform/metadata/{{identity.entity.aliases.K8S_ACCESSOR.metadata.service_account_namespace}}/*" {
  capabilities = ["read", "list"]
}
path "apps/data/{{identity.entity.aliases.K8S_ACCESSOR.metadata.service_account_namespace}}/*" {
  capabilities = ["read"]
}
path "apps/metadata/{{identity.entity.aliases.K8S_ACCESSOR.metadata.service_account_namespace}}/*" {
  capabilities = ["read", "list"]
}

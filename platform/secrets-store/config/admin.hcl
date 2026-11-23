# Platform admins (Authelia group `admins`, through scripts/bao.sh).
path "*" {
  capabilities = ["create", "read", "update", "patch", "delete", "list", "sudo"]
}

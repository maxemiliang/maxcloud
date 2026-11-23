#!/bin/sh
# Applies OpenBao's mounts, policies and logins from this directory. Runs as
# the Job openbao-config (../config.yaml) whenever a file here changes, with
# the `config` login that self-init made (../release.yaml). Idempotent: every
# step either checks first or overwrites.
set -eu

export BAO_ADDR="${BAO_ADDR:-https://openbao.openbao.svc:8200}"
dir=$(dirname "$0")

jwt=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=config jwt="$jwt")
export BAO_TOKEN

# KV v2 mounts: platform/<namespace>/<secret> and apps/<namespace>/<secret>.
for mount in platform apps; do
  if ! bao read "sys/mounts/$mount" >/dev/null 2>&1; then
    echo "enabling kv-v2 at $mount/"
    bao secrets enable -path="$mount" -version=2 kv
  fi
done

accessor=$(bao read -field=accessor sys/auth/kubernetes)
sed "s/K8S_ACCESSOR/$accessor/g" "$dir/ns-reader.hcl" | bao policy write ns-reader -
bao policy write controller "$dir/controller.hcl"
bao policy write admin "$dir/admin.hcl"

bao write auth/kubernetes/role/reader \
  bound_service_account_names=eso-reader \
  bound_service_account_namespaces='*' \
  token_policies=ns-reader \
  token_ttl=15m token_max_ttl=1h

bao write auth/kubernetes/role/controller \
  bound_service_account_names=maxcloud-controller \
  bound_service_account_namespaces=maxcloud-system \
  token_policies=controller \
  token_ttl=1h token_max_ttl=4h

# Break-glass: the same admin policy for a token of the ServiceAccount
# openbao/openbao-admin, which only cluster admins can make (kubectl create
# token). For when Authelia is down or not set up yet (its secrets live
# here), and in place of recovery keys, which a self-initialized OpenBao
# doesn't have. scripts/bao.sh --break-glass.
bao write auth/kubernetes/role/break-glass \
  bound_service_account_names=openbao-admin \
  bound_service_account_namespaces=openbao \
  token_policies=admin \
  token_ttl=15m token_max_ttl=15m

# Admins log in with the Authelia ID token mcloud already has (client
# `kubernetes`, the same one the API server trusts). Skipped when no issuer
# is set (test clusters).
if [ -n "${OIDC_ISSUER:-}" ]; then
  if ! bao read sys/auth/oidc >/dev/null 2>&1; then
    echo "enabling jwt auth at oidc/"
    bao auth enable -path=oidc jwt
  fi
  bao write auth/oidc/config \
    oidc_discovery_url="$OIDC_ISSUER" \
    bound_issuer="$OIDC_ISSUER" \
    default_role=admin
  bao write auth/oidc/role/admin - <<JSON
{
  "role_type": "jwt",
  "bound_audiences": ["kubernetes"],
  "user_claim": "preferred_username",
  "groups_claim": "groups",
  "bound_claims": {"groups": ["admins"]},
  "token_policies": ["admin"],
  "token_ttl": "1h",
  "token_max_ttl": "1h"
}
JSON
fi

echo "OpenBao configured"

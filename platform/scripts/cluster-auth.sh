#!/usr/bin/env bash
# Shows or applies the cluster's OIDC authentication (UKS cluster
# authentication, UpCloud API public beta) from
# infra/cluster-auth/authentication.yaml.
#
#   scripts/cluster-auth.sh show
#   scripts/cluster-auth.sh apply
#
# Needs UPCLOUD_TOKEN: an API token of the homelab_api sub-account (it has
# managed_kubernetes). Changes take up to a minute to reach the API server.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

api=https://api.upcloud.com/1.3
: "${UPCLOUD_TOKEN:?set UPCLOUD_TOKEN to an API token of the homelab_api sub-account}"
cluster=${CLUSTER_NAME:-homelab}

uuid=$(curl -fsS -H "Authorization: Bearer $UPCLOUD_TOKEN" "$api/kubernetes" |
  jq -er --arg n "$cluster" '[.[] | select(.name == $n)] | if length == 1 then .[0].uuid else error("no single cluster named \($n)") end')

case "${1:-}" in
show)
  curl -fsS -H "Authorization: Bearer $UPCLOUD_TOKEN" "$api/kubernetes/$uuid/authentication" | jq .
  ;;
apply)
  curl -fsS -X PUT \
    -H "Authorization: Bearer $UPCLOUD_TOKEN" \
    -H "Content-Type: application/yaml" \
    --data-binary @infra/cluster-auth/authentication.yaml \
    "$api/kubernetes/$uuid/authentication/import" | jq .
  echo "Applied to cluster $cluster ($uuid). It takes up to a minute; then: kubectl auth whoami" >&2
  ;;
*)
  echo "usage: $0 show|apply" >&2
  exit 2
  ;;
esac

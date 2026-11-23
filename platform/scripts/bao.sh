#!/usr/bin/env bash
# Runs the OpenBao CLI as a platform admin:
#   scripts/bao.sh kv get -mount=platform monitoring/grafana-oidc
#   printf %s "$token" | scripts/bao.sh kv put -mount=platform external-dns/cloudflare-api-token api-token=-
#   scripts/bao.sh kv put -mount=platform authelia/authelia-users - <users.json
#
# It logs in with your Authelia ID token (mcloud login's, via `mcloud
# credential`; OpenBao's role `admin` takes members of the group admins) and
# runs `bao` inside the OpenBao pod, so nothing needs installing and
# OpenBao needs no route from outside. The token goes over stdin, never on
# a command line. Whatever you pipe in reaches bao's stdin after it, for
# values given as `-`.
#
# scripts/bao.sh --break-glass ... (or BAO_BREAK_GLASS=1, which other
# scripts pass through) logs in as the ServiceAccount
# openbao/openbao-admin instead (a 10-minute token from kubectl, so cluster
# admins only): for when Authelia is down or not set up yet. OpenBao's audit
# log then shows the ServiceAccount, and the API server's who asked for it.
#
# The mounts: platform/<namespace>/<secret> (the platform's Secrets,
# platform/secrets) and apps/<namespace>/app-secrets (mcloud secrets).
# After changing a platform value, make ESO pick it up now rather than
# within the hour, and restart what reads it:
#   kubectl -n <ns> annotate externalsecret <secret> force-sync=$(date +%s) --overwrite
set -euo pipefail

(($#)) || { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' >&2; exit 2; }

login="auth/oidc/login role=admin"
if [[ $1 == --break-glass || ${BAO_BREAK_GLASS:-} == 1 ]]; then
  [[ $1 != --break-glass ]] || shift
  jwt=$(kubectl -n openbao create token openbao-admin --duration=10m)
  login="auth/kubernetes/login role=break-glass"
else
  jwt=$(mcloud credential | yq -r '.status.token')
  [[ -n $jwt && $jwt != null ]] || { echo "bao.sh: no ID token; run mcloud login (or --break-glass)" >&2; exit 1; }
fi

# shellcheck disable=SC2016 # expanded inside the pod
{ printf '%s\n' "$jwt"; [[ -t 0 ]] || cat; } |
  kubectl -n openbao exec -i openbao-0 -c openbao -- sh -c '
    read -r jwt
    BAO_TOKEN=$(printf %s "$jwt" | bao write -field=token $0 jwt=-) || exit 1
    export BAO_TOKEN
    exec bao "$@"' "$login" "$@"

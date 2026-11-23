#!/usr/bin/env bash
# Generates Authelia's secrets and its one user, and writes them to OpenBao
# (through scripts/bao.sh), where External Secrets Operator makes the Secrets:
#   platform/authelia/authelia-secrets   random keys + OIDC signing key
#   platform/authelia/authelia-users     the user (argon2id password hash)
#   platform/monitoring/grafana-oidc     Grafana's OIDC client secret
# Nothing secret is printed. Prompts for the user's details and password.
# Needs docker (for the authelia CLI), openssl, yq, kubectl and mcloud
# (logged in as an admin).
#
# Re-running replaces all three: every session is logged out, and the
# registered passkeys/TOTP become unreadable (new storage encryption key), so
# they have to be registered again.
#
# On a brand-new platform there is no admin login yet (Authelia needs these
# secrets first): write them with `kubectl exec` into openbao-0 as described
# in the README's Secrets section ("bootstrapping Authelia").
set -euo pipefail
cd "$(dirname "$0")/.."

image=ghcr.io/authelia/authelia:4.39.28

authelia() { docker run --rm "$image" authelia "$@"; }
rand() { openssl rand -hex 32; }
# Value after "<label>: " in the authelia CLI's output.
field() { sed -n "s/^$1: //p"; }

# Writes a JSON object of plain values from stdin to platform/<path>, and
# has ESO update the Secret now.
put() {
  local namespace=$1 name=$2
  scripts/bao.sh kv put -mount=platform "$namespace/$name" - >/dev/null
  kubectl -n "$namespace" annotate externalsecret "$name" force-sync="$(date +%s)" --overwrite >/dev/null 2>&1 || true
  echo "wrote platform/$namespace/$name"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

read -rp "Username: " username
read -rp "Display name: " displayname
read -rp "Email: " email
read -rsp "Password: " password
echo
read -rsp "Password again: " password2
echo
[[ $password == "$password2" ]] || { echo "Passwords don't match." >&2; exit 1; }

echo "Hashing (argon2id)..."
password_hash=$(authelia crypto hash generate argon2 --password "$password" | field Digest)
unset password password2

cat >"$tmp/users_database.yml" <<EOF
users:
  ${username}:
    disabled: false
    displayname: '${displayname}'
    email: '${email}'
    password: '${password_hash}'
    groups:
      - admins
EOF

# Grafana gets the secret, Authelia only its hash.
client=$(authelia crypto hash generate pbkdf2 --variant sha512 --random --random.length 72 --random.charset rfc3986)
client_secret=$(field "Random Password" <<<"$client")
client_digest=$(field Digest <<<"$client")

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$tmp/oidc-jwks-key.pem" 2>/dev/null

# Values reach yq through the environment, never a command line.
SESSION=$(rand) STORAGE=$(rand) RESET=$(rand) HMAC=$(rand) DIGEST=$client_digest PEM=$tmp/oidc-jwks-key.pem \
  yq -n -o json '{
    "session-secret": strenv(SESSION),
    "storage-encryption-key": strenv(STORAGE),
    "reset-password-jwt-secret": strenv(RESET),
    "oidc-hmac-secret": strenv(HMAC),
    "grafana-client-secret-digest": strenv(DIGEST),
    "oidc-jwks-key.pem": load_str(strenv(PEM))
  }' | put authelia authelia-secrets

USERS=$tmp/users_database.yml yq -n -o json '{"users_database.yml": load_str(strenv(USERS))}' |
  put authelia authelia-users

SECRET=$client_secret yq -n -o json '{"client-secret": strenv(SECRET)}' | put monitoring grafana-oidc

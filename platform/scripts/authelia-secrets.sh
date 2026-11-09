#!/usr/bin/env bash
# Generates Authelia's secrets and its one user, and writes them SOPS-encrypted:
#   infrastructure/controllers/authelia/authelia-secrets.sops.yaml  random keys + OIDC signing key
#   infrastructure/controllers/authelia/authelia-users.sops.yaml    the user (argon2id password hash)
#   infrastructure/controllers/monitoring/grafana-oidc.sops.yaml    Grafana's OIDC client secret
# Nothing secret is printed. Prompts for the user's details and password.
# Needs docker (for the authelia CLI), openssl, kubectl and sops.
#
# Re-running replaces all three files: every session is logged out, and the
# registered passkeys/TOTP become unreadable (new storage encryption key), so
# they have to be registered again.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

image=ghcr.io/authelia/authelia:4.39.28
authelia_dir=infrastructure/controllers/authelia
monitoring_dir=infrastructure/controllers/monitoring

authelia() { docker run --rm "$image" authelia "$@"; }
rand() { openssl rand -hex 32; }
# Value after "<label>: " in the authelia CLI's output.
field() { sed -n "s/^$1: //p"; }

# Builds a Secret manifest with kubectl (nothing is sent to the cluster) and
# encrypts it with the rules in .sops.yaml.
encrypt_secret() {
  local namespace=$1 name=$2 out=$3
  shift 3
  kubectl -n "$namespace" create secret generic "$name" "$@" --dry-run=client -o yaml |
    sops encrypt --filename-override "$out" >"$out"
  echo "wrote $out"
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

encrypt_secret authelia authelia-secrets "$authelia_dir/authelia-secrets.sops.yaml" \
  --from-literal=session-secret="$(rand)" \
  --from-literal=storage-encryption-key="$(rand)" \
  --from-literal=reset-password-jwt-secret="$(rand)" \
  --from-literal=oidc-hmac-secret="$(rand)" \
  --from-literal=grafana-client-secret-digest="$client_digest" \
  --from-file=oidc-jwks-key.pem="$tmp/oidc-jwks-key.pem"

encrypt_secret authelia authelia-users "$authelia_dir/authelia-users.sops.yaml" \
  --from-file=users_database.yml="$tmp/users_database.yml"

encrypt_secret monitoring grafana-oidc "$monitoring_dir/grafana-oidc.sops.yaml" \
  --from-literal=client-secret="$client_secret"

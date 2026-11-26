#!/usr/bin/env bash
# claude-history's credentials, written to OpenBao (through scripts/bao.sh):
#
#   platform/claude-history/clickhouse-users
#       CLICKHOUSE_INGEST_PASSWORD_SHA256: the SHA-256 of the ingest user's
#       password, which is all ClickHouse gets (users.xml). ESO makes it the
#       Secret clickhouse-users (secrets/claude-history.yaml).
#   platform/claude-history/laptop
#       What the laptops' Vector needs (dotfiles: claude-history/README.md):
#       ingest_password, and the claude-history-writer S3 key from
#       infra/storage's outputs (s3_access_key_id, s3_secret_access_key).
#       No ExternalSecret reads it: it never becomes a Secret in the
#       cluster.
#
#   claude-history-secrets.sh          a new ingest password, and the S3 key
#   claude-history-secrets.sh --s3     only the S3 key (after rotating it in
#                                      infra/storage), keeping the password
#
# The S3 key needs `tofu -chdir=infra/storage output` to work
# (TF_VAR_state_passphrase and the state bucket's keys in the environment).
# Without it, the laptop path gets the password only; run --s3 once tofu
# works.
#
# Nothing secret is printed, and values reach bao and yq on stdin or
# through the environment, never on a command line. Needs openssl, yq,
# kubectl and mcloud (logged in as an admin), like scripts/bao.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

s3_only=false
[[ ${1:-} == --s3 ]] && s3_only=true

s3_key_id=
s3_secret=
if s3_key_id=$(tofu -chdir=infra/storage output -raw claude_history_writer_access_key_id 2>/dev/null) &&
  s3_secret=$(tofu -chdir=infra/storage output -raw claude_history_writer_secret_access_key 2>/dev/null); then
  :
else
  s3_key_id= s3_secret=
  if $s3_only; then
    echo "tofu -chdir=infra/storage output doesn't work here: no S3 key to write." >&2
    exit 1
  fi
  echo "warning: no S3 key (tofu -chdir=infra/storage output failed); run with --s3 once it works." >&2
fi

# Writes a JSON object from stdin to platform/claude-history/<name>, and
# has ESO update the Secret of that name now, if there is one.
put() {
  local name=$1
  scripts/bao.sh kv put -mount=platform "claude-history/$name" - >/dev/null
  kubectl -n claude-history annotate externalsecret "$name" force-sync="$(date +%s)" --overwrite >/dev/null 2>&1 || true
  echo "wrote platform/claude-history/$name"
}

if $s3_only; then
  KEY_ID=$s3_key_id SECRET=$s3_secret \
    yq -n -o json '{"s3_access_key_id": strenv(KEY_ID), "s3_secret_access_key": strenv(SECRET)}' |
    scripts/bao.sh kv patch -mount=platform claude-history/laptop - >/dev/null
  echo "updated the S3 key in platform/claude-history/laptop"
  exit 0
fi

password=$(openssl rand -hex 32)
HASH=$(printf %s "$password" | sha256sum | cut -d' ' -f1) \
  yq -n -o json '{"CLICKHOUSE_INGEST_PASSWORD_SHA256": strenv(HASH)}' | put clickhouse-users
PASSWORD=$password KEY_ID=$s3_key_id SECRET=$s3_secret \
  yq -n -o json '{"ingest_password": strenv(PASSWORD), "s3_access_key_id": strenv(KEY_ID), "s3_secret_access_key": strenv(SECRET)}' |
  put laptop
unset password

echo "Next: on each laptop, step 2 of claude-history/README.md in the dotfiles. ClickHouse reads the hash at start:"
echo "  kubectl -n claude-history rollout restart sts/clickhouse"

#!/usr/bin/env bash
# Adds, removes and lists the people who can log in (Authelia's user file),
# in OpenBao at platform/authelia/authelia-users (through scripts/bao.sh).
# External Secrets Operator turns it into the Secret authelia-users, and
# Authelia reads the file when it starts:
#   kubectl -n authelia rollout restart deploy/authelia
# (everyone logs in again; passkeys and TOTP are kept).
#
#   scripts/developer.sh add <username> [--name 'Display Name'] [--email a@b.c] [--admin] [--hash '$argon2id$...']
#   scripts/developer.sh passwd <username> [--hash '$argon2id$...']
#   scripts/developer.sh remove <username>
#   scripts/developer.sh list
#   scripts/developer.sh code
#
# add puts the user in the group maxcloud-developers (may run apps on
# maxcloud: infrastructure/configs/auth/developers.yaml), or admins with
# --admin (runs the cluster). It makes up a first password and prints it,
# once, for you to pass on. With --hash it stores a hash the developer made
# themselves instead, so nobody else ever knows the password:
#   docker run --rm -it ghcr.io/authelia/authelia authelia crypto hash generate argon2
#
# The first login asks the developer to register a passkey or TOTP, which
# takes a one-time code Authelia "sends by email": into a file, here. code
# prints the latest one (it reads the running Authelia; nothing changes).
#
# Needs mcloud (logged in as an admin), yq and kubectl; add and passwd
# without --hash also docker (for the authelia CLI) and openssl.
set -euo pipefail
cd "$(dirname "$0")/.."

path=authelia/authelia-users
key=users_database.yml
image=$(sed -n 's/^ *image: *\(ghcr\.io\/authelia\/authelia:[^ ]*\).*/\1/p' infrastructure/controllers/authelia/deployment.yaml | head -n1)

usage() {
  sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' >&2
  exit 2
}
die() {
  echo "developer.sh: $*" >&2
  exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
db=$tmp/$key

# Reads the file and its version from OpenBao (the first user comes from
# scripts/authelia-secrets.sh).
load() {
  scripts/bao.sh kv get -mount=platform -format=json "$path" >"$tmp/kv.json" </dev/null ||
    die "can't read platform/$path from OpenBao"
  version=$(yq -p json -r '.data.metadata.version' "$tmp/kv.json")
  KEY=$key yq -p json -r '.data.data[strenv(KEY)]' "$tmp/kv.json" >"$db"
  rm -f "$tmp/kv.json"
  yq -e '.users | type == "!!map"' "$db" >/dev/null || die "platform/$path has no users"
}

# Writes it back, only if nobody changed it since load (check-and-set),
# and has ESO update the Secret now.
save() {
  scripts/bao.sh kv put -mount=platform -cas="$version" "$path" "$key=-" <"$db" >/dev/null ||
    die "writing platform/$path failed (changed by someone else since? run it again)"
  kubectl -n authelia annotate externalsecret authelia-users force-sync="$(date +%s)" --overwrite >/dev/null
  echo "wrote platform/$path" >&2
}

exists() { USERNAME=$1 yq -e '.users | has(strenv(USERNAME))' "$db" >/dev/null 2>&1; }

# A username becomes the Kubernetes user authelia:<username> and an app's
# owner in the registry.
check_username() {
  [[ $1 =~ ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ ]] || die "username '$1': lowercase letters, digits and dashes, starting with a letter, 2 to 32 characters"
}

# Prints a password hash: the given one, or a made-up password's (which goes
# to stderr, once).
password_hash() {
  local hash=$1 username=$2 password out
  if [[ -n $hash ]]; then
    [[ $hash == \$argon2id\$* ]] || die "--hash: an argon2id hash, as 'authelia crypto hash generate argon2' prints after 'Digest: '"
    printf '%s' "$hash"
    return
  fi
  [[ -n $image ]] || die "no authelia image found in infrastructure/controllers/authelia/deployment.yaml"
  password=$(openssl rand -base64 18 | tr '+/' '-_')
  out=$(docker run --rm "$image" authelia crypto hash generate argon2 --password "$password") || die "hashing the password failed"
  hash=$(sed -n 's/^Digest: //p' <<<"$out")
  [[ $hash == \$argon2id\$* ]] || die "unexpected output from the authelia CLI"
  {
    echo
    echo "First password for $username (shown once; pass it on over something private):"
    echo "  $password"
    echo
  } >&2
  printf '%s' "$hash"
}

after() {
  cat >&2 <<EOF
Next:
  1. kubectl -n authelia get externalsecret authelia-users   (SecretSynced, a few seconds)
  2. kubectl -n authelia rollout restart deploy/authelia
EOF
}

cmd=${1:-}
[[ -n $cmd ]] || usage
shift

case $cmd in
add)
  username=${1:-}
  [[ -n $username && $username != -* ]] || usage
  shift
  name=$username email="" group=maxcloud-developers hash=""
  while (($#)); do
    case $1 in
    --name) name=${2:?--name needs a value}; shift 2 ;;
    --email) email=${2:?--email needs a value}; shift 2 ;;
    --hash) hash=${2:?--hash needs a value}; shift 2 ;;
    --admin) group=admins; shift ;;
    *) usage ;;
    esac
  done
  check_username "$username"
  # Authelia wants an address for every user; nothing is sent to it while
  # the notifier writes to a file.
  [[ -n $email ]] || email=$username@users.invalid
  load
  exists "$username" && die "$username is already a user (passwd changes the password, remove takes them out)"
  what="their username and first password"
  [[ -z $hash ]] || what="their username"
  hash=$(password_hash "$hash" "$username")
  USERNAME=$username NAME=$name EMAIL=$email HASH=$hash GROUP=$group yq -i '
    .users[strenv(USERNAME)] = {
      "disabled": false,
      "displayname": strenv(NAME),
      "email": strenv(EMAIL),
      "password": strenv(HASH),
      "groups": [strenv(GROUP)]
    }' "$db"
  save
  echo "added $username to $group" >&2
  after
  cat >&2 <<EOF
  3. Send $username $what, and point them at docs/mcloud.md ("Getting access").
     When they register a passkey, Authelia asks for a one-time code: scripts/developer.sh code
EOF
  ;;
passwd)
  username=${1:-}
  [[ -n $username && $username != -* ]] || usage
  shift
  hash=""
  while (($#)); do
    case $1 in
    --hash) hash=${2:?--hash needs a value}; shift 2 ;;
    *) usage ;;
    esac
  done
  load
  exists "$username" || die "no user $username"
  hash=$(password_hash "$hash" "$username")
  USERNAME=$username HASH=$hash yq -i '.users[strenv(USERNAME)].password = strenv(HASH)' "$db"
  save
  after
  ;;
remove)
  username=${1:-}
  [[ -n $username && $# -eq 1 ]] || usage
  load
  exists "$username" || die "no user $username"
  [[ $(yq '.users | length' "$db") -gt 1 ]] || die "$username is the only user: nobody could log in"
  USERNAME=$username yq -i 'del(.users[strenv(USERNAME)])' "$db"
  save
  echo "removed $username" >&2
  after
  cat >&2 <<EOF
  Their logins stop working within an hour of the restart (the ID token's
  life). Apps they own keep them as an owner until someone changes that:
  mcloud apps list, mcloud apps owners <app> --remove $username
EOF
  ;;
list)
  [[ $# -eq 0 ]] || usage
  load
  yq '.users | to_entries | .[] | [.key, (.value.groups | join(",")), (.value.disabled == true)] | join("  ")' "$db" |
    sed -e 's/  true$/  disabled/' -e 's/  false$//'
  ;;
code)
  [[ $# -eq 0 ]] || usage
  kubectl -n authelia exec deploy/authelia -- cat /data/notification.txt
  ;;
*)
  usage
  ;;
esac

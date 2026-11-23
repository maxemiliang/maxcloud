#!/usr/bin/env bash
# One-time move of the platform's SOPS Secrets (private/secrets/**/*.sops.yaml)
# into OpenBao, at platform/<namespace>/<name> (README, "Secrets",
# migration). For each file it:
#   1. decrypts it (sops, with the platform's age key) and writes its data
#      to OpenBao with check-and-set 0, so a value already there is never
#      overwritten (re-running is safe);
#   2. marks the Secret in the cluster kustomize.toolkit.fluxcd.io/prune=disabled,
#      so the old `secrets` Kustomization doesn't delete it when it stops
#      listing it. ESO takes the Secret over (creationPolicy Owner) and the
#      mark no longer matters.
# Values only travel through pipes: nothing decrypted touches the disk.
#
#   scripts/sops-to-bao.sh            # every file
#   scripts/sops-to-bao.sh --dry-run  # only list what it would copy
#
# Needs sops (with the age key), yq, kubectl, mcloud (logged in as an
# admin) and OpenBao running (platform/secrets-store).
set -euo pipefail
cd "$(dirname "$0")/.."

dry_run=false
[[ ${1:-} == --dry-run ]] && dry_run=true

dir=../private/secrets
[[ -d $dir ]] || { echo "$dir is missing: git submodule update --init --checkout private" >&2; exit 1; }

find "$dir" -name '*.sops.yaml' | sort | while read -r file; do
  ns=$(yq -r '.metadata.namespace' "$file")
  name=$(yq -r '.metadata.name' "$file")
  [[ -n $ns && $ns != null && -n $name && $name != null ]] || { echo "skip $file: no metadata.namespace/name" >&2; continue; }
  keys=$(yq -r '((.data // {}) + (.stringData // {})) | keys | join(",")' "$file")
  echo "$file -> platform/$ns/$name ($keys)"
  $dry_run && continue

  # data is base64, stringData plain; OpenBao keeps plain strings.
  sops decrypt "$file" |
    yq -o json '((.data // {}) | map_values(@base64d)) + (.stringData // {})' |
    scripts/bao.sh kv put -mount=platform -cas=0 "$ns/$name" - >/dev/null 2>"${TMPDIR:-/tmp}/sops-to-bao.err" ||
    if grep -q 'check-and-set' "${TMPDIR:-/tmp}/sops-to-bao.err"; then
      echo "  already in OpenBao, left as it is"
    else
      cat "${TMPDIR:-/tmp}/sops-to-bao.err" >&2
      exit 1
    fi
  kubectl -n "$ns" annotate secret "$name" kustomize.toolkit.fluxcd.io/prune=disabled --overwrite >/dev/null
done
rm -f "${TMPDIR:-/tmp}/sops-to-bao.err"

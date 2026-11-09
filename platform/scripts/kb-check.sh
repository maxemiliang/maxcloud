#!/usr/bin/env bash
# Checks the knowledge base (docs/kb/): every entry has the frontmatter the
# kb skill (.claude/skills/kb/SKILL.md) asks for, is listed in the index,
# and its relative links and `where:` paths exist.
set -euo pipefail
cd "$(dirname "$0")/.."

kb=docs/kb
index=$kb/README.md
fail=0
err() {
  echo "kb: $*" >&2
  fail=1
}

for f in "$kb"/*/*.md; do
  [ -e "$f" ] || continue
  rel=${f#"$kb"/}
  if [ "$(head -1 "$f")" != "---" ]; then
    err "$rel: no frontmatter"
    continue
  fi
  fm=$(awk 'NR==1{next} /^---$/{exit} {print}' "$f")
  for key in title kind area tags learned updated; do
    grep -q "^$key:" <<<"$fm" || err "$rel: frontmatter has no $key:"
  done
  kind=$(sed -n 's/^kind: *\([a-z]*\).*/\1/p' <<<"$fm")
  case "$kind" in
    gotcha | decision | fact) ;;
    *) err "$rel: kind must be gotcha, decision or fact" ;;
  esac
  area=$(sed -n 's/^area: *\([a-z0-9-]*\).*/\1/p' <<<"$fm")
  [ "$area" = "$(dirname "$rel")" ] || err "$rel: area: $area doesn't match its directory"
  grep -qF "]($rel)" "$index" || err "$rel: not listed in $index"
  # where: paths, relative to the repo root.
  while read -r p; do
    [ -e "$p" ] || err "$rel: where: $p doesn't exist"
  done < <(awk 'NR==1{next} /^---$/{exit} /^where:/{w=1; next} w && /^  - /{sub(/^  - /,""); print; next} w{w=0}' "$f")
  # Relative Markdown links.
  while read -r link; do
    [ -e "$(dirname "$f")/$link" ] || err "$rel: link $link doesn't exist"
  done < <(grep -o '](\([^)#]*\))' "$f" | sed 's/^](//; s/)$//' | grep -v '^[a-z]*:' || true)
done

# Index lines that point nowhere.
while read -r link; do
  [ -e "$kb/$link" ] || err "$index: $link doesn't exist"
done < <(grep -o '](\([^)]*\.md\))' "$index" | sed 's/^](//; s/)$//' || true)

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "kb: $(find "$kb" -mindepth 2 -name '*.md' | wc -l) entries OK"

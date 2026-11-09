#!/usr/bin/env bash
# Regenerates JSON schemas in scripts/schemas/ for CRDs that the community
# catalog validate.sh uses doesn't have, or has an outdated copy of. Run it
# after bumping one of the charts below (validate.sh fails on fields the old
# schema doesn't know), and commit the result.
#
# Needs helm (mise.toml) and python3 with PyYAML.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
out=scripts/schemas
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Knative Operator's KnativeServing, at the version pinned in its HelmRelease.
version=$(grep -oP 'version: "\K[^"]+' infrastructure/controllers/knative-operator/release.yaml)
helm template knative-operator knative-operator \
  --repo https://knative.github.io/operator --version "$version" \
  --show-only templates/crds/knativeservings.yaml >"$work/crds.yaml"

python3 -I - "$work/crds.yaml" "$out" <<'PY'
import json, os, sys
import yaml

def strict(node):
    """kubeconform -strict: reject unknown fields, like the API server does."""
    if isinstance(node, dict):
        if node.get("x-kubernetes-int-or-string"):
            node.pop("type", None)
            node["oneOf"] = [{"type": "string"}, {"type": "integer"}]
        if "properties" in node and "additionalProperties" not in node \
                and not node.get("x-kubernetes-preserve-unknown-fields"):
            node["additionalProperties"] = False
        for v in node.values():
            strict(v)
    elif isinstance(node, list):
        for v in node:
            strict(v)

src, out = sys.argv[1], sys.argv[2]
for crd in yaml.safe_load_all(open(src)):
    if not crd:
        continue
    group, kind = crd["spec"]["group"], crd["spec"]["names"]["kind"].lower()
    for version in crd["spec"]["versions"]:
        schema = version["schema"]["openAPIV3Schema"]
        # apiVersion/kind/metadata are checked by kubeconform itself.
        for field in ("apiVersion", "kind", "metadata"):
            schema.setdefault("properties", {}).setdefault(field, {})
        strict(schema)
        os.makedirs(f"{out}/{group}", exist_ok=True)
        path = f"{out}/{group}/{kind}_{version['name']}.json"
        with open(path, "w") as f:
            json.dump(schema, f, indent=1, sort_keys=True)
            f.write("\n")
        print(path)
PY

---
title: kustomize edit rewrites lists unindented
kind: gotcha
area: build-deploy
tags: [kustomize, maxcloud, mcloud]
learned: 2026-11-04
updated: 2026-11-04
where:
  - templates/run-app/deploy/k8s/kustomization.yaml
---

# kustomize edit rewrites lists unindented

**Symptom:** a functions app's second deploy failed in run-deploy's pin
step: `Error: invalid Kustomization: yaml: line 7: did not find expected key`.

**Cause:** the first deploy's `kustomize edit set image` rewrote
`kustomization.yaml` with unindented list items (`- functions.yaml`).
`mcloud generate` only recognised indented items, didn't see
`functions.yaml`, and listed it again indented: mixed indentation, invalid
YAML.

**Fix:** mcloud's kustomization editing accepts both layouts (maxcloud
`759c9c3`), with a test for kustomize's own format. Anything else that edits
a kustomization.yaml as text must too.

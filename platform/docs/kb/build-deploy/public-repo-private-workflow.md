---
title: A public repo can't call a private repo's reusable workflow
kind: gotcha
area: build-deploy
tags: [github-actions, reusable-workflows, maxcloud]
learned: 2026-11-04
updated: 2026-11-04
where:
  - templates/run-app/.github/workflows/deploy.yaml
---

# A public repo can't call a private repo's reusable workflow

**Symptom:** an app repo's deploy run fails at once, "This run likely
failed because of a workflow file issue": `workflow was not found` for
`maxemiliang/homelab/.github/workflows/run-deploy.yaml@main`.

**Cause:** the Actions access setting on a private repo opens its workflows
only to the owner's other private repos. Public repos can't use them.

**Fix:** `run-deploy` lives in the public maxemiliang/maxcloud repo; app
repos call `maxemiliang/maxcloud/.github/workflows/run-deploy.yaml@main`.

**Recognise it:** no job runs at all; the error is only on the run's page
(`gh run view` says "workflow file issue").

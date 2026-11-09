---
title: mcloud build and dev, things that weren't obvious
kind: gotcha
area: build-deploy
tags: [mcloud, docker, buildx, pack, knative, mise]
learned: 2026-11-06
updated: 2026-11-06
where:
  - templates/run-app/.github/workflows/deploy.yaml
---

# mcloud build and dev, things that weren't obvious

Code: maxcloud repo, `internal/cli/build.go` and
`internal/functions/functions.go`.

- **Knative refuses `fieldRef` env vars** unless the
  `kubernetes.podspec-fieldref` feature flag is on, so `MAXCLOUD_APP` is
  written into each function's manifest as a literal, not from
  `metadata.namespace`.
- **A mise shim makes `pack` look installed:** `exec.LookPath("pack")`
  finds `~/.local/share/mise/shims/pack` even with no version set, and it
  then fails. mcloud checks `pack --version` and otherwise runs pack in a
  container (`buildpacksio/pack`).
- **This laptop's default buildx builder is a container**
  (`container-builder`, docker-container driver), so it can't reach the
  host's 127.0.0.1: a test push to a local registry needs
  `BUILDX_BUILDER=default`. The real registry is unaffected.
- **Docker credential helpers inherit docker's environment**, which is how
  mcloud hands its push token to itself running as
  `docker-credential-mcloud` (a symlink in a throwaway `DOCKER_CONFIG`). A
  `credsStore` would win over `auths`, so mcloud uses `credHelpers` for the
  registry instead of writing a login.
- **run-deploy installs the newest mcloud** at run time and calls
  `mcloud build --ci`; release mcloud before pushing a workflow that needs
  a new mcloud feature.

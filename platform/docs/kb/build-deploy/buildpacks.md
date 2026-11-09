---
title: Building with Paketo buildpacks: what apps need
kind: fact
area: build-deploy
tags: [buildpacks, paketo, nodejs, go]
learned: 2026-11-04
updated: 2026-11-08
where:
  - templates/run-app/README.md
---

# Building with Paketo buildpacks: what apps need

- **TypeScript:** Paketo runs no build script unless told to. A
  `project.toml` with `BP_NODE_RUN_SCRIPTS=build` does it (`mcloud init`
  writes it). A `package-lock.json` makes it use `npm ci`.
- **Node.js start:** the image runs `npm start` through `start.sh`, which
  forwards SIGTERM, so an app that handles it exits 0.
- **Go:** builds the main package at the repo root with the Go version from
  `go.mod` (newest matching). It stamps VCS info, so building needs a git
  repo with at least one commit (`failed to execute 'git rev-parse HEAD'`).
- **Images run as uid 1002**, which Pod Security `restricted` accepts.
- **Where it runs:** `mcloud build` runs pack with the builder
  `paketobuildpacks/ubuntu-noble-builder`, locally, in CI (run-deploy) or in
  the cluster, and pushes to the maxcloud registry
  ([own-image-registry](own-image-registry.md)), not GHCR.
- **Locally**, if Docker reports `all predefined address pools have been fully
  subnetted`, `pack build --network host` gets around it.

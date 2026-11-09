---
title: mise -C <repo> exec runs the command inside the repo
kind: gotcha
area: tooling
tags: [mise, kubeconfig, kind]
learned: 2026-11-04
updated: 2026-11-04
where:
  - mise.toml
---

# mise -C <repo> exec runs the command inside the repo

`mise -C ~/.t3/projects/homelab exec -- <cmd>` changes into the repo
before running `<cmd>`, and the repo's mise.toml sets `KUBECONFIG` to the
homelab cluster. So relative paths resolve inside the repo, not where you
are, and kubectl talks to the real cluster even when you meant a local kind
cluster. Use the tool's binary directly (`mise which kustomize`), absolute
paths, and an explicit `--kubeconfig`.

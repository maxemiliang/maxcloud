---
title: A killed tofu apply leaves a stale state lock
kind: gotcha
area: ops
tags: [opentofu, state, s3, lock]
learned: 2026-11-01
updated: 2026-11-01
where:
  - infra/cluster/main.tf
---

# A killed tofu apply leaves a stale state lock

**Symptom:** after a `tofu apply` was killed halfway (the T3 server
restarted), the next run fails on the state lock, and the plan shows a
deposed object.

**Fix:** `tofu force-unlock <id>`, then `tofu apply -refresh-only` to bring
state in line, then plan again.

**Prevent it:** run long applies so they survive the session (in their own
terminal, or detached), not as a tool call that a restart can kill.

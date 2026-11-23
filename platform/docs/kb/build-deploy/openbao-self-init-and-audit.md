---
title: OpenBao self-init can't enable an audit device, and a failed self-init needs empty storage
kind: gotcha
area: build-deploy
tags: [openbao, self-init, audit, seal, recovery-keys, postgres]
learned: 2026-11-22
updated: 2026-11-22
where:
  - secrets-store/release.yaml
  - secrets-store/config/configure.sh
---

# OpenBao self-init can't enable an audit device, and a failed self-init needs empty storage

**Symptom:** on first start, OpenBao (2.7.3) unseals, revokes the root
token, then logs:

```
[ERROR] failed to initialize:
  | initialize.[audit (0)]: request.[enable-audit (0)]: failed to evaluate request: 1 error occurred:
  | \t* invalid request
```

The pod restarts in a loop, and nothing can log in.

**Cause:** OpenBao 2.x refuses to create audit devices through the API,
and self-init's `request`s go through the API. It only takes them from the
config file. A failed self-init leaves the storage initialized but
unconfigured, with the root token already revoked and no recovery keys
(self-init never makes them).

**Fix:**
- The audit device is an `audit "file" "stdout"` block in the server
  config (`secrets-store/release.yaml`).
- Self-init only gives the config Job a login. The Job sets up everything
  else (`config/configure.sh`), and it can run again.
- The way back from a failed self-init is empty storage: delete the
  Cluster `openbao-db` and its PVC (only while it holds nothing yet), and
  let Flux make it again (README, Secrets; `docs/runbooks/openbao.md`).
- In place of recovery keys there's the `break-glass` login
  (`scripts/bao.sh --break-glass`).

**Recognise it:** `kubectl -n openbao logs openbao-0 | grep -A3 'failed to initialize'`.
`bao status` shows `Total Recovery Shares 0`; that much is normal.

See also: [openbao-secrets](openbao-secrets.md).

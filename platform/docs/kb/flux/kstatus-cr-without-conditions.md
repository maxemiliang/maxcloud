---
title: Flux counts a CR without conditions as ready
kind: gotcha
area: flux
tags: [flux, kstatus, health-checks, knative]
learned: 2026-11-04
updated: 2026-11-04
where:
  - clusters/personal/infrastructure.yaml
---

# Flux counts a CR without conditions as ready

**Symptom:** `infra-configs` went Ready before Knative was installed, and
`apps` failed with `webhook not found` for Knative's webhook.

**Cause:** Flux's health checks (kstatus) treat a custom resource without
status conditions as ready. A KnativeServing has none until the operator
first reconciles it.

**Fix:** a `healthCheckExprs` entry on `infra-configs` for KnativeServing:
ready only once `Ready=True`. Add one for any other CR whose readiness
`apps` depends on.

---
title: Knative rejects pod-level runAsUser; set it on the container
kind: gotcha
area: knative
tags: [knative, pod-security, securitycontext]
learned: 2026-11-04
updated: 2026-11-04
where:
  - templates/run-app/deploy/k8s/service.yaml
---

# Knative rejects pod-level runAsUser; set it on the container

**Symptom:** the Knative webhook rejects a Service whose pod
`securityContext` sets `runAsUser`.

**Cause:** Knative only allows a subset of PodSpec fields; pod-level
`runAsUser` isn't in it by default.

**Fix:** set `securityContext.runAsUser` on the container instead. Knative
fills in the rest of the `restricted` Pod Security settings by itself.
Buildpacks images already run as a non-root user (uid 1002), so they need
nothing.

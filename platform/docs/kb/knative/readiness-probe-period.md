---
title: A readinessProbe with periodSeconds makes Knative pods Ready up to 10 s late
kind: gotcha
area: knative
tags: [knative, queue-proxy, readiness, probe, cold-start, rollout, mcloud, healthPath]
learned: 2026-11-09
updated: 2026-11-09
where:
  - apps/maxcloud/resourceset.yaml
---

# A readinessProbe with periodSeconds makes Knative pods Ready up to 10 s late

**Symptom:** an app's pod logs "listening" within a couple of seconds, but
turns Ready (and gets requests) up to 10 s later. On a cold start from zero
the first request waits that long in the activator; every rollout waits too.
paja's `web` (2026-11-09): listening at 10:34:07.4, Ready at 10:34:16. maxoo's
`web`, without a probe period: Ready the same second it listened.

**Cause:** Knative moves the app's readinessProbe into the queue-proxy
(`SERVING_READINESS_PROBE`), which the kubelet probes every 10 s. What the
queue-proxy does with each kubelet probe depends on `periodSeconds`
(knative/serving 1.24, `pkg/queue/readiness/probe.go`,
`shouldProbeAggressively() = PeriodSeconds == 0`):

- unset (0): it retries the app every 50 ms, for up to 10 s, until it
  answers. Ready within ~50 ms of the app.
- set: it checks the app once. An app that comes up just after a kubelet
  probe waits for the next one, up to `periodSeconds` later.

mcloud gave every function with a `healthPath` `periodSeconds: 10` (from
`e7dbaf3`) until the fix below.

**Fix:** leave `periodSeconds` off readiness probes on Knative services
(`internal/functions/functions.go` in the maxcloud repo; its test checks it).
The liveness probe keeps its own period. Apps pick it up on their next
deploy, which regenerates their `functions.yaml`.

See also: [ready-2-2-and-error-pods](ready-2-2-and-error-pods.md) (cold
start times).

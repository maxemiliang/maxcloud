---
title: READY 2/2 is app plus queue-proxy; Error pods mean the app ignored SIGTERM
kind: fact
area: knative
tags: [knative, queue-proxy, sigterm, pid1]
learned: 2026-11-04
updated: 2026-11-09
where:
  - templates/run-app/README.md
---

# READY 2/2 is app plus queue-proxy; Error pods mean the app ignored SIGTERM

- A Knative pod has two containers, the app and Knative's `queue-proxy`
  sidecar: `READY 2/2` is one instance, not two. A cold start scales to one
  pod (Prometheus' desired/actual replicas peak at 1).
- Pods that show `Error` while scaling down: the app exited non-zero on
  SIGTERM. An app that doesn't handle it runs as PID 1, where the kernel
  ignores unhandled signals; it's killed after the grace period (Go exits 2).
  Harmless for a web service, but a worker loses its job in hand.
- Fix in the app: catch SIGTERM, stop the server, exit 0 (template README,
  "Shutting down"). The maxcloud SDKs do it for you.
- Paketo's Node.js image runs `npm start` through a `start.sh` that forwards
  SIGTERM; a Paketo Go image runs the binary directly as PID 1.
- Cold start, measured 2026-11-04: ~0.75-0.9 s (Go whoami), ~1.3 s (Node
  functions app) over HTTPS from zero; scale to zero after ~86 s idle. A
  readinessProbe with `periodSeconds` adds up to 10 s
  ([readiness-probe-period](readiness-probe-period.md)).

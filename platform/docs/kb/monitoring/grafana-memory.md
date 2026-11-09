---
title: Grafana 13 needs a memory limit above 384Mi
kind: fact
area: monitoring
tags: [grafana, memory, oom]
learned: 2026-10-31
updated: 2026-10-31
where:
  - infrastructure/controllers/monitoring/kube-prometheus-stack.yaml
---

# Grafana 13 needs a memory limit above 384Mi

Grafana 13 idles at about 330Mi and was OOM-killed at a 384Mi limit while
loading dashboards. It runs with at least 384Mi requested and a 1Gi limit.

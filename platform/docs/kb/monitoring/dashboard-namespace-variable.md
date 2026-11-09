---
title: kube_namespace_labels only has allow-listed labels
kind: gotcha
area: monitoring
tags: [prometheus, kube-state-metrics, grafana]
learned: 2026-11-04
updated: 2026-11-04
where:
  - infrastructure/configs/monitoring/dashboards/run-platform.json
---

# kube_namespace_labels only has allow-listed labels

**Symptom:** a Grafana variable listing app namespaces by their
`run.gronblom.dev/tenant` label came out empty.

**Cause:** kube-state-metrics exports only allow-listed namespace labels in
`kube_namespace_labels`.

**Fix:** the run-platform dashboard lists app namespaces as
`label_values(kube_resourcequota{resourcequota="tenant"}, namespace)`: every
app namespace has the registry's `tenant` quota.

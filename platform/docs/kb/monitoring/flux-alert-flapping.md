---
title: FluxResourceNotReady re-fired while the failure never changed
kind: gotcha
area: monitoring
tags: [alertmanager, telegram, flux, flux-operator, prometheus, flapping]
learned: 2026-11-07
updated: 2026-11-07
where:
  - infrastructure/configs/monitoring/rules.yaml
---

# FluxResourceNotReady re-fired while the failure never changed

**Symptom:** Telegram gets "resolved" and then, about 15 minutes later,
"firing" for the same Flux object, at odd hours, with nothing fixed in
between. Seen 2026-11-07 for the `paja` Kustomization (resolved 02:51 and
14:22 UTC, re-fired 03:08 and 14:39).

**Cause:** `flux_resource_info` has `ready`, `reason` and `revision` as
labels, so every change of those starts a new series. A Kustomization that
keeps failing reports `ready="Unknown"` (`reason="Progressing"`) for the
fraction of a second each retry takes. A scrape that lands there is a gap in
the `ready="False"` series: the old expression returned nothing, the alert
resolved, and the 15m `for` started again. A new `revision` did the same.

**Fix:** the rule aggregates to one series per object,
`max by (exported_namespace, kind, name) (flux_resource_info{ready!="True", suspended="False"})`,
and has `keep_firing_for: 10m` for missed scrapes. Normal deploys are not
Ready for at most ~6 minutes, well inside `for: 15m`. The `reason` label is
gone from the summary (it was usually just `ReconciliationFailed`); the error
itself is only in the object's Ready condition, not in any metric.

**Recognise it:** in Prometheus, `flux_resource_info{name="<name>"}` shows a
one-sample `ready="Unknown"` series in the gap of the `ready="False"` one.

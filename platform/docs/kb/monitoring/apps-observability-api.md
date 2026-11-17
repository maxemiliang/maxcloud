---
title: App owners read their logs, metrics and alerts through the maxcloud controller, not from Loki or Prometheus
kind: decision
area: monitoring
tags: [maxcloud, loki, prometheus, alertmanager, rbac, tokenreview, cloudflare, security]
learned: 2026-11-17
updated: 2026-11-17
where:
  - infrastructure/controllers/maxcloud/config.yaml
  - infrastructure/controllers/maxcloud/gateway.yaml
---

# App owners read their logs, metrics and alerts through the maxcloud controller, not from Loki or Prometheus

**Decision:** `mcloud logs --since`, `mcloud metrics` and `mcloud alerts`
call the maxcloud controller's apps API
(`https://maxcloud-api.gronblom.dev/api/v1/apps/<app>/{logs,metrics,alerts}`,
GET only), on the API gateway ([api-gateway](../ops/api-gateway.md)): a
request gets to it only with a login that passed the gateway's check, so
made-up tokens are turned away before anything is asked of the API server.
For each request the controller then:

1. checks the bearer token (the developer's Authelia ID token, the one
   their kubeconfig sends) with a TokenReview;
2. checks with a SubjectAccessReview that the user may `get pods/log`
   (logs) or `list pods` (metrics, alerts) in the app's namespace, which
   only its owners and admins may;
3. refuses namespaces without `run.gronblom.dev/tenant`;
4. builds every Loki and Prometheus query itself, with
   `namespace="<app>"` in each selector. The client picks the time range,
   a pod-name regex and a container, quoted into that one selector, never
   the query.

Alerts come from Prometheus's `/api/v1/rules`, not Alertmanager, and only
from PrometheusRules in `monitoring` (by the rule file's UID; the
controller may list PrometheusRules). An alert is the app's when its
`namespace` or `k8s_namespace_name` label is the app, or its `namespace`
is `maxcloud-system` and its `app` label is the app
(`MaxcloudDatabaseBlocked`).

**Why:** Loki, Prometheus and Alertmanager aren't multi-tenant, and app
pods can't reach them anyway ([tenant-egress-deny](../networking/tenant-egress-deny.md)).
The controller already serves a public endpoint, reaches the monitoring
namespace (no NetworkPolicies there or on `maxcloud-system`'s egress), and
the API server already decides who owns what (the registry's per-app
RoleBindings). TokenReview and SubjectAccessReview reuse that instead of
a second list of owners.

**Rejected:**

- `services/proxy` on the monitoring namespace for developers: a proxy to
  Loki or Prometheus answers any query, so any app's logs and metrics.
- Turning on Loki's multi-tenancy (`auth_enabled`, a tenant per namespace)
  and a tenant-enforcing proxy: more moving parts, and Prometheus would
  still need the same proxy.
- Alerts from Alertmanager: its alerts don't say which PrometheusRule made
  them.
- Showing alerts from apps' own PrometheusRules (apps may write them,
  `infrastructure/configs/run/rbac.yaml`): Prometheus doesn't confine their
  queries to the app (no `enforcedNamespaceLabel`), so an app's rule can
  put another app's numbers into an alert labelled with its own namespace.

**Known limits:**

- It was first served next to the discovery document, on
  `maxcloud.gronblom.dev`. That hostname is proxied by Cloudflare, which
  would have seen every ID token (valid for an hour, for the API server
  too), and nothing limited how many unknown tokens were put to the API
  server. Moved to the gateway's hostname, which is DNS-only, on
  2026-11-17, before it was rolled out.
- An app's own PodMonitor with `honorLabels: true` can write series with
  another app's `namespace` label. That can put wrong numbers or alerts
  into the other app's view (as it already can on Grafana); it can't read
  the other app's data.
- Owners aren't told by push. `mcloud alerts --json` on a schedule is the
  stopgap. Per-app notifications need the owner of the platform to decide
  where they go (see Revisit).

**Revisit when:** per-app notifications are wanted. The contained way is
an Alertmanager route per app that the controller writes (not apps:
receivers make Alertmanager send HTTP to wherever they say, which reaches
Loki's delete API), to a destination an admin picks: the Telegram bot
into a chat the app's owners give, or email from Authelia's SMTP.
Also when Prometheus gets `enforcedNamespaceLabel`: then apps' own rules'
alerts could be shown too.

See also: [tenant-egress-deny](../networking/tenant-egress-deny.md),
[maxcloud-app-owners-and-registrations](../data/maxcloud-app-owners-and-registrations.md).

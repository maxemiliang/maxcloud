---
title: Developers reach the API server through a gateway in the maxcloud controller, not by getting their address allowlisted
kind: decision
area: ops
tags: [gateway, api-server, mcloud, login, authelia, onboarding, admin_cidrs, traefik, spdy]
learned: 2026-11-17
updated: 2026-11-17
where:
  - infrastructure/controllers/maxcloud/gateway.yaml
  - infrastructure/controllers/maxcloud/config.yaml
  - ../controller/internal/gateway/gateway.go
  - ../internal/cli/platform.go
  - infrastructure/configs/monitoring/maxcloud.yaml
  - infra/cluster/cluster.tf
  - scripts/developer.sh
---

# Developers reach the API server through a gateway in the maxcloud controller, not by getting their address allowlisted

**Decision:** `https://maxcloud-api.gronblom.dev` is the API server for
anyone whose address isn't in `admin_cidrs`. The maxcloud controller serves
it (`MAXCLOUD_GATEWAY_URL`, port 8083): a reverse proxy that passes a
request on only when it carries a person's login the API server accepts,
and passes it on with that same token. `mcloud login` writes the gateway
into its kubeconfig when it can't open a connection to the API server
(`--via auto`, the default), and the API server itself when it can.
`admin_cidrs` stays, for admins at home and for break-glass.

**Why:**

- **Everything mcloud does is a Kubernetes API call**, and the API server
  only takes connections from `admin_cidrs` and the NAT gateway. Adding a
  developer meant a `tofu apply` per address, and a laptop on another
  network was locked out. With the gateway a developer needs a login and
  nothing else.
- **The API server's rules still decide everything.** The gateway adds no
  credentials: the caller's token goes upstream unchanged, so
  authentication, RBAC, admission and the audit log are what they are on a
  direct connection, and the controller's own rights are never lent out.
  It also strips `X-Remote-*` headers.
- **Requests without a valid login stop at the gateway.** It first reads
  what the token claims, unverified: issuer, audience and expiry have to
  be Authelia's for the client `kubernetes`. That turns away scanners and
  ServiceAccount tokens without a call. A token that passes is put to the
  API server once, as its bearer (a SelfSubjectReview, which every
  authenticated user may create, so the controller needs no extra RBAC),
  and the username has to start with `authelia:`. The answer is cached:
  yes until the token expires (at most 5 minutes), no for 30 seconds.
  Unknown tokens are checked at most 5 a second (burst 20); past that the
  gateway answers 429, and people already let in aren't affected.
- **What it does not do:** it isn't a second authorization layer. A valid
  login gets exactly what it gets directly. What changes is the exposure:
  before, a stolen token was useless from outside `admin_cidrs`; now it
  works from anywhere until it expires (an hour; the refresh token 12).
  The second factor on the Authelia client `kubernetes` is what stands
  between a leaked password and the cluster.

**It also serves the apps API** (`/api/v1/apps/`, apps' logs, metrics
and alerts for their owners), which the controller answers itself instead
of passing on: the same check comes first, so that API is behind the
unknown-token limit too, and off the Cloudflare-proxied discovery host
([apps-observability-api](../monitoring/apps-observability-api.md)).

**Rejected:**

- **Opening the API server to `0.0.0.0/0`:** every unauthenticated request
  would reach it, and the variable's validation refuses it on purpose.
- **A TCP passthrough on Traefik to the API server:** the same exposure as
  opening it, plus an SNI the kubeconfig has to fake.
- **A VPN or tailnet:** another account and client per developer, which is
  the kind of step this removes.
- **Verifying the token's signature in the gateway:** it would need
  Authelia's keys (network access from the controller, a cache, rotation),
  and the API server verifies every request anyway. Reading the claims
  unverified is enough to decide whether asking is worth it.
- **Behind Cloudflare's proxy:** `exec` and `port-forward` upgrade to
  SPDY, which Cloudflare doesn't carry, and it cuts streams that are
  quiet for 100 seconds (watches, `logs -f`). The route is DNS-only, like
  the registry's.

**Not verified on the live cluster yet (2026-11-17):** SPDY upgrades
through Traefik to the gateway. The gateway's own tests cover the upgrade
and the stream; Traefik passing `Upgrade: SPDY/3.1` on is expected (it
proxies any upgrade) but unmeasured here. After rollout, check with the
gateway kubeconfig: `mcloud login --via gateway`, then `mcloud db psql`
(a port-forward) and `mcloud sandbox ssh` (an exec).

**Revisit when:** the controller's single replica becomes a problem (a
restart cuts every stream through it, and the gateway is down while it
restarts: `upctl kubernetes config` is the break-glass, as before), or
when a second login provider appears (the gateway takes one issuer).

The alert `MaxcloudGatewayThrottled`
(`infrastructure/configs/monitoring/maxcloud.yaml`) fires when unknown
tokens keep arriving faster than the gateway checks them: someone is
sending made-up ones.

See also: [admin-certificate-not-system-masters](admin-certificate-not-system-masters.md),
[web-console](web-console.md) (people's tokens passed on from a pod, for the browser).

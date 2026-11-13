---
title: Traefik's Gateway API provider rejects ExternalName backends
kind: gotcha
area: networking
tags: [traefik, gateway-api, knative, sso, authelia, maxcloud, traefikservice, buckets]
learned: 2026-11-04
updated: 2026-11-07
where:
  - apps/run-demo/sso.yaml
  - templates/run-app/deploy/examples/sso.yaml
  - infrastructure/controllers/traefik/release.yaml
---

# Traefik's Gateway API provider rejects ExternalName backends

**Symptom:** an HTTPRoute that sends `*.lab.gronblom.dev` (SSO) traffic to a
Knative service doesn't work: Knative's per-service Services are ExternalName
Services, and Traefik refuses them as backends.

**Cause:** Traefik's Gateway API provider doesn't support ExternalName
Services as backendRefs.

**Fix:** the SSO route's backend is Service `traefik/traefik-knative`
(Traefik's own in-cluster entrypoint), with a `URLRewrite` filter setting the
Host to the service's cluster-local name, so Traefik routes it again like any
cluster-local request. A ReferenceGrant in `traefik` allows the app
namespace's routes to use that Service; the app registry creates it for
every app.

**Reaching a host outside the cluster (public buckets, 2026-11-05):** the
same limit applies. The maxcloud controller routes
`<bucket>--<app>.makiya.dev` to Object Storage through Traefik's own
objects instead, all in `maxcloud-system`: an HTTPRoute whose backendRef is
a TraefikService, which points at an ExternalName Service
(`passHostHeader: false`, https). For that, Traefik has:

- `providers.kubernetesCRD.allowExternalNameServices: true`;
- `providers.kubernetesGateway.crossProviderNamespaces: [maxcloud-system]`.
  This is the only way an HTTPRoute can name a TraefikService: as
  `<namespace>_<name>@kubernetescrd` (safe naming, since controller
  v0.12.0).

**Trap: the name depends on `safeNaming`.** Its legacy naming (off)
turns every run of non-alphanumeric characters into one `-`, so
TraefikService `maxcloud-system/mc-run-demo--cdn` was
`maxcloud-system-mc-run-demo-cdn@kubernetescrd`. That let names collide
(`mc-shop--x-y` and `mc-shop-x--y`; app Middlewares too; 2026-11-06 audit,
M4). Safe naming is on now: `maxcloud-system_mc-run-demo--cdn@kubernetescrd`,
as written. The controller must use the same scheme
(`MAXCLOUD_TRAEFIK_SAFE_NAMING`), or a wrong name gives Traefik's plain
`404 page not found` (no router), and Traefik logs `the service
"...@kubernetescrd" does not exist`. Public bucket hosts 404 briefly while
the flip rolls out, until the controller rewrites the routes.

**Recognise it:** a 404 with body `404 page not found` (Traefik's), not an
S3 XML error. `kubectl -n traefik logs ds/traefik | grep 'does not exist'`.

See also: [knative/external-probe-port](../knative/external-probe-port.md),
[upcloud-object-storage-behaviour](../data/upcloud-object-storage-behaviour.md).

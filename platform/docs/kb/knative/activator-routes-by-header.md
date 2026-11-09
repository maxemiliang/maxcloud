---
title: Knative's activator serves any app's revision to whoever names it
kind: gotcha
area: knative
tags: [knative, activator, traefik, cilium, maxcloud, security, net-gateway-api]
learned: 2026-11-07
updated: 2026-11-08
where:
  - infrastructure/configs/run/tenant-egress.yaml
  - infrastructure/controllers/traefik/release.yaml
  - infrastructure/configs/run/tenant-routing.yaml
---

# Knative's activator serves any app's revision to whoever names it

**Symptom:** an app reaches another app's private (cluster-local) service
without going through Traefik. Either of these, from any app pod, got the
other app's answer (kind, 2026-11-07):

```
curl -H 'Knative-Serving-Namespace: paja' -H 'Knative-Serving-Revision: web-00001' http://activator-service.knative-serving
curl -H 'Host: web-00001.paja.svc.cluster.local' http://activator-service.knative-serving
```

The headers also work when they come through Traefik. Take an app's own
route to its own revision while the activator is in front of it (scaled to
zero, or `target-burst-capacity` "-1"): the client's
`Knative-Serving-*` headers pass through, and the activator serves the
other app.

**Cause:** the activator picks the revision from the request's
`Knative-Serving-Namespace` and `Knative-Serving-Revision` headers. Without
them, it splits a Host of `<revision>.<ns>.svc.<cluster domain>`. It
doesn't check who's calling
([`context_handler.go`](https://github.com/knative/serving/blob/release-1.24/pkg/activator/handler/context_handler.go)).
net-gateway-api sets both headers in each route's backend filter. Revision
names are guessable (`<service>-00001`). Behind the activator, the app's
pods see `knative-serving` as the source. So checks at the destination
(NetworkPolicies, a mesh's authorization) can't tell callers apart either
([Knative's Istio guide](https://knative.dev/docs/serving/istio-authorization/)).

**Fix (audit M3, 2026-11-08), three parts:**

- Apps can't connect to `knative-serving` (`tenant-egress.yaml`). They
  never need to: Traefik is what calls the activator.
- Traefik removes `Knative-Serving-Namespace`/`-Revision` from every
  incoming request: a `headers` Middleware on every entrypoint, in the
  Traefik release's `extraObjects`. Knative's routes set them again in their
  backend filters, which run after.
- An app's routes may set `Knative-Serving-Namespace` only to the app
  itself, and its Middlewares not at all (`tenant-routing.yaml`).

**Recognise it:** Hubble drops from an app to the activator after the fix:
`hubble observe --to-namespace knative-serving --verdict DROPPED`. A
request through Traefik with forged headers now gets the activator's
`404 Error getting active endpoint: revision.serving.knative.dev "<x>" not found`.

See also: [networking/tenant-egress-deny](../networking/tenant-egress-deny.md),
`private/docs/kb/networking/traefik-ingress-host-rules.md`,
[custom-domains-copy-route](custom-domains-copy-route.md).

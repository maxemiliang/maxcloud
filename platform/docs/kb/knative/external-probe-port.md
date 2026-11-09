---
title: Knative's external probe needs Traefik's plain-HTTP in-cluster port
kind: gotcha
area: knative
tags: [knative, traefik, gateway-api, net-gateway-api]
learned: 2026-11-04
updated: 2026-11-04
where:
  - infrastructure/configs/gateway/gateway.yaml
  - infrastructure/configs/knative/serving.yaml
  - infrastructure/controllers/traefik/release.yaml
---

# Knative's external probe needs Traefik's plain-HTTP in-cluster port

**Symptom:** a public Knative service never gets Ready: `Waiting for load
balancer to be ready` forever, though the route works. net-gateway-api's
probes get 404.

**Cause:** net-gateway-api probes the external Gateway through a Service.
Given Service `traefik`, it picked the websecure port (8443), where a
plain-HTTP probe can't match a route.

**Fix:** the public Gateway has an `in-cluster` listener on Traefik's
`knative` entrypoint (8081, plain HTTP), and Knative's external gateway is
configured with `service: traefik/traefik-knative`, a ClusterIP Service for
that entrypoint. After changing `config-gateway`, restart
`net-gateway-api-controller`; it doesn't pick the change up by itself.

**Recognise it:** `kubectl -n <app> get ingress.networking.internal.knative.dev -o yaml`
shows LoadBalancerReady Unknown, and the net-gateway-api controller logs probe
failures with status 404.

A new public service shows `Waiting for load balancer` for a minute or two
normally, while the probe catches up and external-dns creates the record.

See also: [traefik-externalname-backends](../networking/traefik-externalname-backends.md).

---
title: maxcloud custom domains copy Knative's route; no DomainMapping
kind: decision
area: knative
tags: [knative, maxcloud, custom-domains, domainmapping, gateway-api, net-gateway-api, traefik, external-dns]
learned: 2026-11-06
updated: 2026-11-06
where:
  - infrastructure/configs/knative/serving.yaml
  - infrastructure/configs/run/domains.yaml
  - infrastructure/configs/run/route-admission.yaml
  - apps/maxcloud/resourceset.yaml
---

# maxcloud custom domains copy Knative's route; no DomainMapping

**Decision (2026-11-06):** a Domain (registry field `domains`) becomes an
HTTPRoute `domain-<host>` on `traefik/public` in the app's namespace,
made by the maxcloud controller. Its rules are a copy of the HTTPRoute
net-gateway-api makes for the service's cluster-local name
(`<svc>.<app>.svc.cluster.local`, labels `serving.knative.dev/route` and
`networking.knative.dev/visibility: cluster-local`), minus the prober's
rule (header `K-Network-Hash: override`). The controller watches those
routes and copies them again on every new revision.

**Why:**

- The backends are the revisions' own Services, with the
  `Knative-Serving-Revision` and `-Namespace` headers the activator needs
  while a service scales from zero. So it works for private services and
  scale-to-zero, in one hop, with the client's Host header untouched.
- No cluster-scoped objects, and no Knative flag to change.

**Rejected:** a Knative DomainMapping. It needs a ClusterDomainClaim per
hostname (cluster-scoped; `autocreate-cluster-domain-claims` is off, and
an app's reconciler is only namespace `admin`). Its KIngress rewrites the
Host to the service's cluster-local name and sends the request through
the local gateway a second time: the app sees
`Host: web.paja.svc.cluster.local`, the original only in
`K-Original-Host` (knative serving, domainmapping/resources/ingress.go).
That's the same as the hand-written URLRewrite route lankkupaja used.

**Also decided:** hostnames are exact, from an admin allowlist
(`domains.yaml`), not under a dedicated subdomain. Two-level names can't
be Cloudflare-proxied with the free edge certificate, and the apps that
asked want their existing names.

**Revisit when:** Knative or net-gateway-api changes the shape of its
routes (the copy would follow anything else in the rules, but the
labels and the prober header are assumptions), or a DomainMapping gets an
option to keep the Host.

See also: [custom-domains-outside-managed-zones](../networking/custom-domains-outside-managed-zones.md),
[admission-policy-params-fail-closed](../ops/admission-policy-params-fail-closed.md).

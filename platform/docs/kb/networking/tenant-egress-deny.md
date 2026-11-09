---
title: Apps can't connect to platform pods (a Cilium deny, not per-namespace allow-lists)
kind: decision
area: networking
tags: [cilium, networkpolicy, traefik, admission, maxcloud, security]
learned: 2026-11-07
updated: 2026-11-08
where:
  - infrastructure/configs/run/tenant-egress.yaml
  - infrastructure/configs/run/tenant-routing.yaml
---

# Apps can't connect to platform pods (a Cilium deny, not per-namespace allow-lists)

**Decision:** one CiliumClusterwideNetworkPolicy, `run-tenant-egress`,
with `egressDeny` from every pod in a `run.gronblom.dev/tenant` namespace
to every pod outside one, except in `traefik` and CoreDNS in
`kube-system`. It also blocks 169.254.169.254, UpCloud's
metadata service. `enableDefaultDeny: false`, so the rest of an app's
egress stays as its own policies say.

**Why:** the 2026-11-06 audit (M2) found Loki (no auth, delete API),
Alertmanager (silences) and the controller's metrics reachable from any
app pod. The alternative was ingress allow-lists in each platform
namespace. That needs every real client to be known, including webhooks
from UKS's managed control plane and kubelet probes, and every new
component needs one. The deny touches only apps. Cilium deny rules beat
allow rules, so an app's own allow-all NetworkPolicy can't undo it.

**What apps still reach:**

- other apps, which have default-deny ingress;
- Traefik, for cluster-local routes and public names;
- the API server, which CloudNativePG's instance manager in the database
  pods needs. It's an identity (`kube-apiserver`), not a namespaced pod,
  so `toEndpoints` doesn't match it.

Hubble on 2026-11-07 showed no other connections that apps start.

**Traefik is the way around it.** Traefik reaches every namespace, and an
app's Middleware decides where Traefik sends requests: a forwardAuth
`address`, or an `errors` middleware's service (an ExternalName Service;
the CRD provider allows those). `tenant-routing.yaml` limits Middlewares:
forwardAuth only to Authelia, and no `errors` or `plugin`. HTTPRoute
backends are safe: the Gateway provider refuses ExternalName Services
([traefik-externalname-backends](traefik-externalname-backends.md)), and
`admin` can't write Endpoints or EndpointSlices.

**Tested** in kind with Cilium 1.20.4. The cluster runs 1.18.6, but its
agent won't start on a 7.x desktop kernel (BPF probe `FnSetRetval`). With
the policy, app pods couldn't reach monitoring or kube-system, even with
an allow-all egress policy of their own. CoreDNS, Traefik, the activator,
other apps, the internet and the API server still answered. Pods outside
app namespaces were unaffected.

**Gotcha, hit on rollout:** a `toEndpoints` selector made only of
`DoesNotExist`/`NotIn` also matches identities that have no namespace
labels at all. Hit: the API server's CIDR identity
(`cidr:100.64.0.0/10,reserved:kube-apiserver`). It's a CIDR identity because
paja's own egress policy names 100.64.0.0/10. The database pods were cut
off from the API server for about 15 minutes. In kind the API server had a
host identity, so the test didn't show it. Fixed with
`k8s:io.kubernetes.pod.namespace: Exists` in the selector. Check with
`hubble observe --verdict DROPPED` after any change to this policy.

**Label to watch:** CoreDNS is allowed by `k8s-app: coredns`, UKS's
label. Upstream and kind use `k8s-app: kube-dns`. If UKS changes it, apps
lose DNS.

**Not the activator (since 2026-11-08, audit M3):** it was allowed "for
cluster-local routes", but apps never call it, Traefik does. And it serves
any app's revision to whoever names it
([activator-routes-by-header](../knative/activator-routes-by-header.md)).

**Apps reaching each other through Traefik:** an app's own routes can't
rewrite the Host to another app's (`tenant-routing.yaml`). The L7 rule on
Traefik's in-cluster entrypoint: `private/docs/kb/networking/traefik-ingress-host-rules.md`.

See also: [admin-role-aggregates-controller-crds](../ops/admin-role-aggregates-controller-crds.md).

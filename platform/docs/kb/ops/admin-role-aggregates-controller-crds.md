---
title: The built-in admin role lets an app drive controllers that are cluster-admin
kind: gotcha
area: ops
tags: [rbac, admission, validatingadmissionpolicy, flux, cert-manager, maxcloud, security]
learned: 2026-11-07
updated: 2026-11-07
where:
  - infrastructure/configs/run/tenant-kinds.yaml
  - apps/maxcloud/resourceset.yaml
---

# The built-in admin role lets an app drive controllers that are cluster-admin

**Symptom:** found in a security audit (2026-11-06), not by an outage.
`kubectl auth can-i create kustomizations -n paja --as=system:serviceaccount:flux-system:paja-reconciler`
says `yes`, and so it does for helmreleases, resourcesets and certificates.

**Cause:** apps are `admin` in their namespace, and `admin` aggregates every
ClusterRole labelled `rbac.authorization.k8s.io/aggregate-to-admin: "true"`.
Flux (`flux-edit-flux-system`), Flux Operator (`flux-operator-edit`) and
cert-manager (`cert-manager-edit`) all ship one. The controllers then act
with their own rights:

- Flux applied a Kustomization, HelmRelease or ResourceSet without
  `serviceAccountName` as itself, cluster-admin (the FluxInstance wasn't
  multitenant then, so there was no `--default-service-account`).
- cert-manager approves any request, and `letsencrypt-prod` solves DNS-01
  for every name in gronblom.dev and lankkupelit.fi.

So one commit to an app repo could take the cluster, or mint a certificate
for `auth.lab.gronblom.dev`.

**Fix:** `infrastructure/configs/run/tenant-kinds.yaml`. In namespaces
labelled `run.gronblom.dev/tenant`, it refuses Flux and Flux Operator
objects, and cert-manager objects whose `issuerRef.kind` is `ClusterIssuer`.
No user is exempt, so cert-manager's own ingress-shim is covered too. The
policies were tested in kind with the live CRDs.

**Recognise it:** before giving apps a new controller, check what it lets
them make:

```sh
kubectl get clusterrole -l rbac.authorization.k8s.io/aggregate-to-admin=true
```

For each, ask: what does the controller do with that object, and as whom?

**Second layer (2026-11-07):** Flux and the Flux Operator run in
multitenant mode ([multitenant-lockdown](../flux/multitenant-lockdown.md)).
A Flux object without `serviceAccountName` now applies as an SA in its own
namespace, so even one that got past the policy can do no more than the app.

**Side effect:** the policy refuses every UPDATE to a Flux object in a
tenant namespace, including the one that removes its finalizer. A Flux
object that's somehow there (made before the policy, or with it switched
off) hangs in deletion until the binding `run-tenant-no-flux` is removed for
a moment. Seen in kind; nothing like it exists live.

See also: [admission-policy-params-fail-closed](admission-policy-params-fail-closed.md),
[multitenant-lockdown](../flux/multitenant-lockdown.md),
[tenant-egress-deny](../networking/tenant-egress-deny.md).

---
title: Flux runs in multitenant mode, so every platform Flux object names a service account
kind: decision
area: flux
tags: [flux, flux-operator, rbac, multitenancy, impersonation, security]
learned: 2026-11-07
updated: 2026-11-07
where:
  - clusters/personal/flux-system/flux-instance.yaml
  - clusters/personal/flux-system/flux-operator-values.yaml
  - infrastructure/controllers/flux-helm.yaml
  - clusters/personal/infrastructure.yaml
---

# Flux runs in multitenant mode, so every platform Flux object names a service account

**Decision:** the FluxInstance has `cluster.multitenant: true` and the Flux
Operator chart has `multitenancy.enabled: true`. It's a second layer behind
the admission policy `run-tenant-no-flux`
(`infrastructure/configs/run/tenant-kinds.yaml`): if a Flux object ever ends
up in an app's namespace, it can do no more than the app itself.

What the controllers do now:

- kustomize-controller and helm-controller apply a Kustomization or
  HelmRelease as its `serviceAccountName`, and one without it as
  `system:serviceaccount:<its namespace>:default`, which has no rights.
  helm-controller impersonates in the HelmRelease's own namespace.
- A `sourceRef` or `chartRef` in another namespace is refused:
  `cross-namespace references have been blocked`. `dependsOn` across
  namespaces still works.
- kustomize-controller refuses remote bases (a URL under `resources`).
- The Flux Operator applies a ResourceSet without `serviceAccountName` as
  `<its namespace>:flux-operator`. In flux-system that's the operator's own SA
  (cluster-admin), so our two ResourceSets are unchanged; anywhere else the
  SA doesn't exist and the apply fails.

So every platform object names its identity:

| Object | serviceAccountName |
|---|---|
| Kustomizations in `clusters/personal` (flux-system) | `kustomize-controller` (cluster-admin through the operator's `cluster-reconciler-flux-system`) |
| The sync Kustomization `flux-system` | set by the operator itself, nothing in git |
| Platform HelmReleases | `flux-helm`, one SA per namespace, `infrastructure/controllers/flux-helm.yaml` |
| HelmRelease `flux-operator` | `flux-operator` (the chart's own) |
| App Kustomizations (generated) | `<app>-reconciler`, admin in the app's namespace only |

**Adding things:** a new platform Kustomization needs `serviceAccountName:
kustomize-controller`; a new platform HelmRelease needs `serviceAccountName:
flux-helm`, and a new namespace needs its `flux-helm` SA and a subject in the
ClusterRoleBinding. A forgotten one fails loudly and harmlessly:

```
failed to retrieve last release from storage: ... User "system:serviceaccount:<ns>:default" cannot list resource "secrets"
```

for a HelmRelease (before Helm touches the release; the workload keeps
running), and `User "system:serviceaccount:flux-system:default" cannot patch
...` for a Kustomization.

**Why:** cheap insurance. Admission already stops apps from making Flux
objects, and everything Flux makes for an app already names a scoped SA.
The lockdown makes the default fail closed if a later change opens a gap: a
namespace without the tenant label, an edited policy, a new controller that
creates Flux objects.

**Rejected:**

- Moving HelmReleases to flux-system with `targetNamespace`: the release name
  and storage change, so helm-controller would install a second copy.
- `tenantDefaultServiceAccount` set to a privileged name that only platform
  namespaces have: an implicit grant to any namespace with that SA name.
- `multitenantObjectIdentity` / `multitenantWorkloadIdentity`: for cloud
  workload identity, which we don't use; needs `objectIdentityRBAC`.

**Lockout and rollback:** the path from git to the FluxInstance can't lock
itself out. The operator reconciles the FluxInstance as itself (never
impersonated) and gives the sync Kustomization `kustomize-controller`. To
undo, revert the commit that set `multitenant: true` (it touches only
`flux-instance.yaml`); the operator re-renders the controllers without the
flags within seconds (kind, 2026-11-07). The operator's lockdown is a
separate one-file commit (`flux-operator-values.yaml`). If Flux can't apply
even the revert, with the admin kubeconfig:

```fish
flux suspend kustomization flux-system
kubectl -n flux-system patch fluxinstance flux --type merge -p '{"spec":{"cluster":{"multitenant":false}}}'
# push the revert, then fetch it BEFORE resuming:
flux reconcile source git flux-system
flux resume kustomization flux-system
```

Resuming before the source has the revert re-applies the last artifact,
`multitenant: true` included: `flux resume` reconciles at once with what it
already has. Seen in kind; the plan in the research doc missed it.

**Tested** in kind (2026-11-07) with this repo itself, not a model: the
FluxInstance synced the branch from a local git server, with dummy SOPS
secrets, self-signed ClusterIssuers and no `wait` on infra-controllers and
infra-configs. Before and after each commit, every Kustomization,
HelmRelease and ResourceSet that could be Ready in kind was Ready, and no
Helm revision changed (external-dns, Loki and Alloy need real cloud
credentials and failed the same way throughout). With both lockdowns on, in
a namespace without the tenant label and with the admission policy
removed: a Kustomization without an SA, one naming an SA that is admin in
its namespace, one with a cross-namespace source, a HelmRelease without an
SA and a ResourceSet without an SA all failed to create a
ClusterRoleBinding or anything in kube-system; an app Kustomization's
reconciler couldn't either. The same tests with the lockdowns off created
both. Note the ResourceSet: without the operator's lockdown, one made a
ClusterRoleBinding for its namespace's `default` SA, and the Flux
lockdown then had nothing left to stop.

**Revisit when:** a Flux minor upgrade changes the multitenant profile
(`flux-operator build instance -f clusters/personal/flux-system/flux-instance.yaml`
shows the rendered flags), or we add notification-controller or the image
controllers, which get `--no-cross-namespace-refs` too.

See also: [admin-role-aggregates-controller-crds](../ops/admin-role-aggregates-controller-crds.md),
[app-kustomizations-no-sops](../build-deploy/app-kustomizations-no-sops.md),
the research in `private/docs/research/flux-multitenant.md`.

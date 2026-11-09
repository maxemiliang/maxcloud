---
title: The cluster's admin certificate is kubeadm:cluster-admins, not system:masters
kind: gotcha
area: ops
tags: [rbac, admission, upcloud, uks, kubectl, kind]
learned: 2026-11-07
updated: 2026-11-08
where:
  - infrastructure/configs/run/registry-admission.yaml
---

# The cluster's admin certificate is kubeadm:cluster-admins, not system:masters

**Symptom:** with the kubeconfig from `upctl kubernetes config` (what this
repo's mise uses), a ValidatingAdmissionPolicy that exempts "admins" by
`system:masters` refuses you anyway, e.g. `ValidatingAdmissionPolicy
'maxcloud-registry-owners' with binding 'maxcloud-registry-owners' denied
request: app demo has no owners: only an admin may change or remove it`.

**Cause:** since Kubernetes 1.29, kubeadm-style admin certificates are
`kubernetes-admin` in the group `kubeadm:cluster-admins` (bound to
cluster-admin), not in `system:masters`. UKS does the same, and so does
kind. RBAC lets that user do anything; a VAP only sees the groups.

**Fix:** exempt `kubeadm:cluster-admins` next to `authelia:admins` and
`system:masters`, as every admin exemption in `registry-admission.yaml`
does (the quota ceiling in `maxcloud-registry-limits` included, since
2026-11-07).

**Recognise it:** `kubectl auth whoami` shows
`Groups [kubeadm:cluster-admins system:authenticated]`.

See also: [maxcloud apps have owners](../data/maxcloud-app-owners-and-registrations.md).

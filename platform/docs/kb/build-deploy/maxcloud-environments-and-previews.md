---
title: maxcloud environments and previews are registry entries <app>--<env>, made only by the controller
kind: decision
area: build-deploy
tags: [maxcloud, registry, environments, previews, admission, flux-operator, security, capacity]
learned: 2026-11-17
updated: 2026-11-17
where:
  - apps/maxcloud/resourceset.yaml
  - infrastructure/configs/run/registry-admission.yaml
  - infrastructure/controllers/maxcloud/environments.yaml
  - infrastructure/controllers/maxcloud/config.yaml
  - infrastructure/controllers/registry/config.json
  - infrastructure/configs/auth/developers.yaml
---

# maxcloud environments and previews are registry entries <app>--<env>, made only by the controller

**Decision:** an app's staging-style environment and its previews are apps
of their own: a registry entry `<app>--<env>` each (`shop--staging`,
`shop--pr-42`), so they get everything an app gets from the ResourceSet
(namespace, quota, network policies, Traefik host rule, owners' bindings,
buckets, database, Flux) with no new templates. Only the maxcloud
controller writes those entries, from what the app's own entry says
(`environments`, `previews*`, written from `maxcloud.yaml`) and from
`Preview` objects in the app's namespace:

- It writes them as the user `maxcloud:environments`, which the developer
  ceiling (`maxcloud-registry-limits`) applies to, so an environment never
  gets more than a developer could ask for. Admission
  (`maxcloud-registry-environments`) lets nobody else write an entry with
  `--` in its name or the `environment-of` label (admins aside), and keeps
  that user off every other entry. Tested on kind (Kubernetes 1.34,
  2026-11-17) with 35 cases, existing-style entries included.
- Names: `--` is already the separator nothing else may contain, so
  `<service>--<app>--<env>.makiya.dev` stays one level deep and
  unambiguous. Env names at most 12, `<app>--<env>` at most 40 (the
  hostname label is 63). Buckets are `mc-<app>--<env>--<bucket>`; the app's
  own S3 policy now denies `mc-<app>--*--*`, so the app can't read its
  environments' buckets (its prefix would cover them).
- Images: environments and previews pull the app's `<app>/**` (zot gives
  `<app>--<env>:pull` read on it) and push nothing. Previews' images go to
  `<app>/<app>--preview`, which zot keeps 14 days (`*/*--preview`; no app
  name has `--`). Retention order matters: that policy is listed before
  `**`.
- GitOps environments deploy `deploy/env/<env>` on their own branch, so
  merges between branches never conflict on deploy commits. A pull
  request's preview deploys the branch `maxcloud/preview/<n>`: one orphan
  commit with only `deploy/k8s`, force-pushed by the deploy workflow.
- Pull requests are listed by a Flux Operator `GitHubPullRequest` input
  provider per app with previews on (`<app>--pull-requests`, filter label
  `preview`, Flux's GitHub App; 0.62.1 supports GitHub App secrets and
  exports `id`, `sha`, `branch`, `author`, `labels`, not whether it's a
  fork). The controller turns its `status.exportedInputs` into `Preview`
  objects `pr-<n>`, and does nothing while the provider isn't Ready.

**Who can cause a preview:** for a pull request, someone who can label it
(GitHub triage or more) *and* someone with write access whose run of the
deploy workflow pushes its image and branch. A fork's run gets no id-token
(and the workflow refuses `pull_request_target`), so a fork's labelled pull
request gets an environment with nothing to deploy. A working-tree preview:
the app's owners only (`previews` is in `maxcloud-app-developer`, not
`admin`, so an app's own manifests can't make one). What a preview gets:
the preview quota (100m/512Mi/10 pods), empty buckets, an empty database
with `previews.db`, its own empty secrets, the app's images; never the
app's secrets, database, buckets or the cluster-local names of the app
(Traefik's host rule is per namespace).

**Why:** reusing the entry keeps one code path for everything an app
gets, and every guardrail (quota, isolation, owners, registrations,
backups) applies without a second copy. The controller is the only writer
because admission can't look up another object: it couldn't check that
whoever makes `shop--x` owns `shop`.

**Rejected:**

- A second ResourceSet generating environments from the app's entry: the
  whole template twice, and the controller's per-entry work (registrations,
  zot, backups) would need to learn a second shape anyway.
- Developers registering `shop--staging` themselves: admission can't tie
  it to `shop`'s owners.
- Previews in the app's own namespace (Knative tags): they'd share its
  secrets, database and buckets, the very thing a pull request mustn't get.
- A copy of production's database for previews (a DatabaseBranch): it needs
  production's backup credentials in the preview's namespace, so anyone who
  can label a pull request reads production data. Previews get an empty
  database; a branch stays an owner's tool in the app's namespace.
- Committing preview manifests to the pull request's own branch: noisy
  commits in the author's branch, and conflicts between pull requests.
- An OCIRepository of manifests in zot: Flux would need pull credentials
  for zot in flux-system, per app.

**Capacity:** each environment is an app's worth of quota (its own, or the
app's); at most 3 per app (`MAXCLOUD_ENVIRONMENTS_MAX`). Previews: at most 6
on the platform (`MAXCLOUD_PREVIEWS_MAX`), `previews.max` per app (at most
3 without an admin), 100m CPU quota each, Knative and queue workers scale
0-1, schedules suspended. A preview's database doesn't scale to zero
(20m CPU, 128Mi requested), so `previews.db` is off by default. Previews
expire `previews.ttl` (default 72h, at most 168h) after their last deploy,
and go, buckets and backups included (`reclaim: Delete` on their entry).

**Rollout gaps to check on the live cluster:** Flux's GitHub App needs
"Pull requests: read"; zot's first-match retention order and UpCloud
deleting a non-empty bucket on `DeleteBucket` weren't tested here.

**Revisit when:** previews should seed data (a sanitised dump, not a
branch), or the CPU budget grows enough for more previews.

See also: [maxcloud-app-owners-and-registrations](../data/maxcloud-app-owners-and-registrations.md),
[own-image-registry](own-image-registry.md),
[drain-cpu-budget](../ops/drain-cpu-budget.md).

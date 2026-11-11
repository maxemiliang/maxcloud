# Knowledge base

What this setup taught us that the code doesn't say: the traps we hit,
the choices we made and why, and how things really behave. One file per
piece of knowledge.

- **Look things up** before debugging or changing a component:
  `grep -ril '<error text or component>' docs/kb/`.
- **Add to it** when something cost time, a decision was made, or you learn
  how something actually works. In Claude Code the `kb` skill
  ([`.claude/skills/kb/SKILL.md`](../../../.claude/skills/kb/SKILL.md)) does it
  and describes the format; `scripts/kb-check.sh` checks it.

Procedures live in [`../runbooks/`](../runbooks/) and the README, not here.

## networking

- [Traefik runs as a DaemonSet with externalTrafficPolicy Local](networking/traefik-daemonset-local-traffic.md): why, and the LB health-check timing
- [*.lab.gronblom.dev is DNS-only; one-level names may be proxied](networking/lab-subdomain-dns-only.md): Cloudflare's free certificate covers one level
- [Traefik's Gateway API provider rejects ExternalName backends](networking/traefik-externalname-backends.md): how SSO routes reach Knative services, and public buckets reach Object Storage (and Traefik's name normalisation)
- [Traefik 3 gives a request 60s, body included; the LB is not the limit](networking/traefik-read-timeout.md): large uploads cut off after a minute
- [maxcloud custom domains cover the managed zones only; other people's domains are a later step](networking/custom-domains-outside-managed-zones.md): why a CNAME alone isn't enough (TLS, listeners, Cloudflare 1014, squatting), and the plan
- [Apps can't connect to platform pods (a Cilium deny, not per-namespace allow-lists)](networking/tenant-egress-deny.md): what apps still reach, and why Middlewares are limited too
- [ClusterIP services time out from gVisor pods; pod IPs and the internet work](networking/gvisor-no-clusterip.md): `npm install` hangs on DNS in a sandbox

## knative

- [Knative's external probe needs Traefik's plain-HTTP in-cluster port](knative/external-probe-port.md): "Waiting for load balancer" forever
- [Knative rejects pod-level runAsUser; set it on the container](knative/pod-security-context.md)
- [Knative's activator and webhook HPAs need caps on a small cluster](knative/hpa-caps.md): and the PDBs that block drains
- [READY 2/2 is app plus queue-proxy; Error pods mean the app ignored SIGTERM](knative/ready-2-2-and-error-pods.md): and measured cold starts
- [maxcloud custom domains copy Knative's route; no DomainMapping](knative/custom-domains-copy-route.md): why, and what a DomainMapping does to the Host
- [A readinessProbe with periodSeconds makes Knative pods Ready up to 10 s late](knative/readiness-probe-period.md): listening in 2 s, Ready in 10; what mcloud's `healthPath` used to do
- [Knative's activator serves any app's revision to whoever names it](knative/activator-routes-by-header.md): `Knative-Serving-Namespace` headers; why apps can't reach it and Traefik strips them

## flux

- [Flux counts a CR without conditions as ready](flux/kstatus-cr-without-conditions.md): "webhook not found" right after install
- [Removing Flux's last annotation on an object can wipe the others](flux/ssa-dropped-annotations.md): "annotation value is immutable" from Knative
- [Render the app registry locally with flux-operator build rset](flux/resourceset-render-locally.md)
- [kubectl get buckets means Flux's Bucket, not maxcloud's](flux/bucket-kind-name-clash.md): use `kubectl get maxcloud -A`
- [A config only a controller writes can deadlock Flux's ordering](flux/controller-written-config-deadlock.md): zot waiting for zot-config; seed it with ssa: IfNotPresent
- [Flux runs in multitenant mode, so every platform Flux object names a service account](flux/multitenant-lockdown.md): `system:serviceaccount:<ns>:default cannot ...` means a missing `serviceAccountName`; rollback

## data

- [UpCloud Object Storage rejects the AWS SDK's default checksums](data/upcloud-s3-checksums.md): the two env vars every S3 client needs
- [How UpCloud Object Storage behaves for per-app users and buckets](data/upcloud-object-storage-behaviour.md): 400 for "already exists", Deny works, public read, presigned PUT
- [How maxcloud databases come back after losing their Cluster or namespace](data/maxcloud-database-recovery.md): measured restore times, and why their disks are deleted
- [maxcloud apps have owners, and their data belongs to one registration](data/maxcloud-app-owners-and-registrations.md): per-app owner bindings, admission, registration IDs, adopting old data

## build-deploy

- [A public repo can't call a private repo's reusable workflow](build-deploy/public-repo-private-workflow.md): why run-deploy lives in maxcloud
- [kustomize edit rewrites lists unindented](build-deploy/kustomize-edit-unindented-lists.md): "did not find expected key"
- [Building with Paketo buildpacks: what apps need](build-deploy/buildpacks.md): TypeScript, Go, uid, which builder
- [maxcloud runs its own registry (zot) instead of GHCR](build-deploy/own-image-registry.md): why, GHCR's limits, and how zot's OIDC logins behave
- [maxcloud apps' secrets live in the cluster, not in git](build-deploy/maxcloud-secrets-in-cluster.md): why not SOPS, never deleted, migrate keeps Flux off
- [mcloud build and dev, things that weren't obvious](build-deploy/mcloud-build-gotchas.md): Knative fieldRef, mise shims, buildx container builders, credential helpers
- [App Kustomizations must not decrypt with the platform's age key](build-deploy/app-kustomizations-no-sops.md): a secretGenerator in an app repo would hand it any homelab secret

## monitoring

- [Grafana 13 needs a memory limit above 384Mi](monitoring/grafana-memory.md)
- [kube_namespace_labels only has allow-listed labels](monitoring/dashboard-namespace-variable.md)
- [FluxResourceNotReady re-fired while the failure never changed](monitoring/flux-alert-flapping.md): one-scrape `ready="Unknown"` blips reset the alert

## ops

- [The built-in admin role lets an app drive controllers that are cluster-admin](ops/admin-role-aggregates-controller-crds.md): Flux objects and ClusterIssuer certificates in app namespaces
- [Everything must fit on one node while the other drains](ops/drain-cpu-budget.md): the CPU budget
- [Node autoscaling, when we add it, is a tainted burst node group for app pods](ops/node-autoscaling-burst-pool.md): the plan, on hold, and UpCloud's autoscaler traps
- [gVisor installs and runs on a UKS node; Kata can't (no nested virtualisation)](ops/sandbox-gvisor-on-uks.md): the sandbox spike's node facts and timings
- [Agent sessions run under gVisor through agent-sandbox, one sandbox per user, and reach the internet but not the cluster](ops/agent-sessions-sandbox.md): why each piece, and what was turned down
- [The sandbox image carries what a session can't install itself, modelled on boat.dev's, at about 2.2 GB](ops/sandbox-image-toolset.md): what's in it, what was left out and why
- [SSH into agent sessions is sshd -i over the exec channel, started by mcloud per connection](ops/agent-sessions-ssh.md): no listener, no new RBAC, host keys without trust on first use
- [A killed tofu apply leaves a stale state lock](ops/tofu-killed-apply.md)
- [Backups stay in one region for now; a copy to de-fra1 is the plan when needed](ops/off-region-backup-copy.md): deferred, with the plan and what it doesn't cover
- [UpCloud sub-account usernames can't contain dashes; Object Storage users can](ops/upcloud-sub-account-names.md): `homelab_api`, `maxcloud_controller`
- [The cluster's admin certificate is kubeadm:cluster-admins, not system:masters](ops/admin-certificate-not-system-masters.md): VAPs that exempt admins must name that group
- [An admission policy's params are fetched before its matchConditions](ops/admission-policy-params-fail-closed.md): "no params found for policy binding", even for exempt users

## tooling

- [mise -C <repo> exec runs the command inside the repo](tooling/mise-exec-changes-directory.md): and talks to the real cluster

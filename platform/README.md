<img src="assets/icon.svg" width="64" height="64" alt="">

# homelab

Personal Kubernetes cluster on UpCloud Managed Kubernetes (UKS, Helsinki),
provisioned with OpenTofu and managed with Flux.

## Layout

```
infra/
  bootstrap/    # one-off: object storage for state + restricted API sub-account (local state)
  cluster/      # network, NAT gateway, router, UKS cluster, node group (s3 backend, encrypted state)
  cluster-auth/ # the API server's OIDC login through Authelia (scripts/cluster-auth.sh applies it)
  flux/         # Flux Operator + FluxInstance + SOPS age key
  storage/      # backup and log buckets + their S3 users, and the maxcloud-apps instance; outlives the cluster (s3 backend, encrypted state)
clusters/
  personal/     # Flux entry point: Kustomizations for infrastructure, apps and the maxcloud controller
infrastructure/
  controllers/  # Traefik, cert-manager, external-dns, kured, CNPG, monitoring, Authelia, Knative Operator, KEDA, metrics-server, the image registry (zot), what the maxcloud controller needs
  configs/      # ClusterIssuers, Gateway, Knative Serving, scrape configs, alert rules, RBAC for people, the run platform's admission policies: what needs the controllers first
apps/
  <app>/        # in-repo apps (hello) and the platform's own app's manifests (run-demo)
  maxcloud/     # app registry for the run platform: the ResourceSet, and entries kept in git (run-demo)
templates/
  run-app/      # starting point for an app repo on the run platform
scripts/        # validate.sh, kb-check.sh, secret generators, cluster-auth.sh, CRD schemas, the Cloudflare IP updater
docs/
  kb/           # knowledge base: gotchas, decisions, facts
  runbooks/     # step-by-step procedures
```

The platform lives in the maxcloud repo next to the code it runs. Its
secrets and the write-ups of open security issues are in a private repo,
checked out (optionally) as the `private/` submodule at the repo root:
`private/secrets/` (what the `secrets` Flux Kustomization applies) and
`private/docs/` (research reports and the kb entries that stay private).

## Tooling

Tool versions are pinned in [`mise.toml`](mise.toml):

```sh
mise install
```

`scripts/validate.sh` checks everything offline (manifests against their
schemas, OpenTofu stacks and lock files, the knowledge base). CI runs it on
every pull request.

Dependency updates come from Renovate ([`renovate.json`](renovate.json)) as
weekly PRs. Nothing is merged automatically, because merging to main deploys.
Renovate is self-hosted: it runs in this repo's GitHub Actions
([`renovate.yaml`](../.github/workflows/renovate.yaml)) as our own GitHub App.

## Secrets

- Local credentials go in `.env` (gitignored, template in `.env.example`) or the
  system keyring (`upctl account login`).
- Kubernetes Secrets are SOPS-encrypted with age, and live only in the
  private repo (`private/secrets/<namespace>/*.sops.yaml`, listed in
  `private/secrets/kustomization.yaml`). Never in this repo, which is public.
  Rules are in [`.sops.yaml`](../.sops.yaml) (and the private repo's own copy).
  To check it out once per clone:
  `git submodule update --init --checkout private && git -C private switch main`.
  Flux doesn't use the submodule: it reads the private repo's `main`
  directly (`clusters/personal/secrets.yaml`), so the submodule's pinned
  commit doesn't matter and `git status` ignores it.
- The age private key lives at `~/.config/sops/age/keys.txt`, with a backup in a
  password manager. It is never committed.

### Break-glass inventory

What must survive losing this laptop. Values live in the password manager, never here.

| Secret | Where it's used | If lost |
|---|---|---|
| `TF_VAR_state_passphrase` | All tofu state (bootstrap, cluster, flux, storage) | State unreadable; rebuild with `tofu import` |
| age private key | SOPS secrets in git, `sops-age` in the cluster | Every `*.sops.yaml` unreadable |
| `homelab_api` sub-account password | Minting API tokens | Reset it as the main account in the Hub |
| UpCloud main account + 2FA recovery | Everything | Account recovery via UpCloud support |

Replaceable, so no backup needed: the UpCloud API token, the S3 state keys
(re-create the access key), the kubeconfig (`upctl kubernetes config`), the
Flux GitHub App private key (generate a new one in GitHub, then `tofu apply` in
`infra/flux`) and the Renovate GitHub App private key (generate a new one, then
`gh secret set RENOVATE_APP_PRIVATE_KEY`). Everything else (Cloudflare tokens,
Telegram bot, Authelia, Grafana, S3 users for backups and logs, the maxcloud
controller's token, S3 key and secrets backup key, the registry's S3 key) is
in `*.sops.yaml` files, so the age key covers it. (Apps' own secrets live in
their namespaces; the maxcloud controller keeps copies encrypted to its
secrets backup key.) The `maxcloud_controller` sub-account's password is in
the password manager. If it's lost, reset it as the main account; its
cluster token expires after 364 days (`UPCLOUD_TOKEN_EXPIRES_AT` in its
secret).

## What runs where

| URL | What | Login |
|---|---|---|
| https://grafana.lab.gronblom.dev | Grafana: metrics (Prometheus), logs (Loki), dashboards | Authelia |
| https://auth.lab.gronblom.dev | Authelia: SSO (OIDC), passkeys/TOTP | user in `authelia-users.sops.yaml` |
| https://paja.lankkupelit.fi | Lankkupaja: run platform app `paja`, from its own repo (`deploy/maxcloud`) | the app's own |
| https://hello.lab.gronblom.dev, https://hello.gronblom.dev | whoami test app | none |
| https://hello--run-demo.gronblom.dev | run platform smoke test: public Knative service (`apps/run-demo`) | none |
| https://run-demo.lab.gronblom.dev | run platform smoke test: private Knative service behind SSO | Authelia |
| https://registry.gronblom.dev | maxcloud's container registry (zot) | an Authelia ID token (`mcloud credential`); admins only for now |

- `*.lab.gronblom.dev` is DNS-only, for admin tools. `*.gronblom.dev` and
  `*.lankkupelit.fi` may be proxied by Cloudflare (SSL mode Full (strict)).
  external-dns creates the records from HTTPRoutes.
- Alerts go to a Telegram group. Alertmanager's always-firing Watchdog pings
  healthchecks.io, which alerts separately if the pings stop (cluster down).
- Prometheus and Alertmanager have no UI route. Use
  `kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090`
  (or `...-alertmanager 9093`).

## Build steps

1. Prereqs and repo skeleton
2. `infra/bootstrap`: state bucket, restricted API sub-account
3. `infra/cluster`: network, NAT, UKS, node group
4. `infra/flux`: Flux Operator bootstrap, SOPS key
5. Platform: Traefik, cert-manager, external-dns, hello
6. kured
7. CloudNativePG and backups (`infra/storage`)
8. Monitoring (kube-prometheus-stack, Loki, Alloy), alerting, Authelia SSO,
   Renovate
9. Apps: lankkupaja (since moved to the run platform as `paja`). Still to do: a
   DR drill (see Known gaps).
10. Run platform: Knative Serving on the existing Traefik, app namespaces with
    guardrails, source deploys from GitHub Actions.
11. Event-driven jobs and workers: KEDA, with each app's Postgres as its queue.
12. maxcloud: the `mcloud` CLI and SDKs (in their own repo), the app
    registry in the cluster, the maxcloud controller (buckets, databases,
    custom domains, secrets backups), its own image registry, and logins
    through Authelia.
13. Security audit fixes (2026-11-06 to 2026-11-08): admission policies for
    what apps may make and route, an egress deny towards the platform, Flux
    in multitenant mode, app owners and registration IDs, and a Traefik L7
    policy on its in-cluster entrypoint.

## Run platform

A self-hosted "Cloud Run": an app repo pushes to `main`, GitHub Actions builds
it, and Flux deploys it as a Knative service that scales to zero when idle.
An app with `deploy: direct` skips git: `mcloud deploy` builds it and
applies its manifests itself.

- **Engine:** Knative Serving, installed by the Knative Operator
  (`infrastructure/controllers/knative-operator`) from the `KnativeServing` in
  `infrastructure/configs/knative/serving.yaml`. No Knative Eventing: its only
  broker that fits here keeps events in memory.
- **Routing:** no second ingress. net-gateway-api turns each Knative service
  into HTTPRoutes on Traefik.
  - Public services go on `traefik/public`, at `<service>--<namespace>.gronblom.dev`.
    That's one level deep, so the existing wildcard certificate and
    Cloudflare's edge certificate cover it. Knative copies the
    `external-dns.kubernetes.io/cloudflare-proxied` annotation to the route.
  - Every service also gets a cluster-local route on `traefik/knative-local`.
    It's served on Traefik's `knative` entrypoint (port 8081), which only the
    ClusterIP Service `traefik-knative` exposes.
  - The public Gateway has an `in-cluster` listener on the same entrypoint, so
    Knative can probe public routes over plain HTTP before it sends traffic to
    a new revision.
- **Private by default:** a service is cluster-local
  (`http://<service>.<namespace>.svc.cluster.local`) unless it has the label
  `run.gronblom.dev/expose: public`.
  - To open a private service in the browser behind Authelia, give it an
    HTTPRoute of its own on `<app>.lab.gronblom.dev` with a ForwardAuth
    Middleware (`templates/run-app/deploy/examples/sso.yaml`, used live in
    `apps/run-demo/sso.yaml`). The hostname must be in `domains.yaml`
    first, and only for an app trusted like an admin: Authelia's cookie
    covers all of `lab.gronblom.dev`, so the app's pods get the session of
    every admin who opens it.
- **Custom domains:** `domains:` in an app's `maxcloud.yaml`
  (`[{host: paja.lankkupelit.fi, service: web, proxied: true}]`) puts a
  Knative service, private ones too, on a hostname of its own.
  - The registry field `domains` becomes Domain objects. The maxcloud
    controller turns each into an HTTPRoute on `traefik/public`, on the
    listener whose certificate covers the hostname. external-dns makes
    the record, proxied by Cloudflare with `proxied: true`.
  - The route copies Knative's own route for the service's cluster-local
    name (revisions, weights, the headers the activator needs) and follows
    it on every new revision. The app sees the Host the client sent, and a
    request takes one hop. A DomainMapping would need a cluster-scoped
    ClusterDomainClaim per hostname, and rewrites the Host.
  - **Who may have which hostname is an admin's call:** the ConfigMap
    `maxcloud-system/maxcloud-domains`
    (`infrastructure/configs/run/domains.yaml`), one key per app.
    Admission checks registry entries against it
    (`registry-admission.yaml`). It also checks every HTTPRoute and
    GRPCRoute made in an app namespace (`route-admission.yaml`): an app
    may use only its cluster-local names and its allowed hostnames, and
    must name them. Before this, any app could put
    a route on any name in gronblom.dev or lankkupelit.fi. Knative's
    controller is exempt; it only makes names from `domain-template`.
  - The controller also refuses a hostname another route on the Gateway
    already has (`HostInUse`, retried every minute), and takes a route
    down when its hostname leaves the allowlist.
  - Only hostnames under a listener's wildcard: `*.gronblom.dev`,
    `*.lab.gronblom.dev`, `*.lankkupelit.fi`, one level deep. Other
    people's domains aren't supported yet
    (`docs/kb/networking/custom-domains-outside-managed-zones.md`).
- **App registry (maxcloud):** `apps/maxcloud/resourceset.yaml` is a Flux
  Operator ResourceSet that generates everything an app gets, once per
  registry entry: a ResourceSetInputProvider in `flux-system` labelled
  `run.gronblom.dev/app` (name, repo, and optional path, branch, quota,
  buckets, db, domains, owners and `deploy: direct`).
  - **Apps register in the cluster, not in this repo.** `mcloud deploy`
    creates the entry from the app's own `maxcloud.yaml` and keeps it in line
    with it; `mcloud apps create|set|delete`
    ([maxcloud](https://github.com/maxemiliang/maxcloud)) change it with your
    kubeconfig. No commit here.
  - Admission checks every entry
    (`infrastructure/configs/run/registry-admission.yaml`): the name may
    not be a namespace the registry doesn't own, `repo`, `path` and
    `branch` must be plain names (they go into the templates), buckets,
    quota and database values must be valid (one bad value would stop the
    ResourceSet for every app), and only admins may register an app kept in
    this repo (no `repo`).
  - **An app belongs to its owners** (the entry's `owners`, Authelia
    usernames): they're admin in its namespace, and only they and admins
    may change or delete its entry (admission). Whoever registers an app
    owns it; `mcloud apps owners <app> --add <user>` adds co-owners. Other
    developers can read that apps exist, not their Secrets or databases.
    An entry without owners (run-demo) is an admin's.
  - **A deleted app's buckets and database backups stay, for that
    registration only.** Each entry has a registration ID (annotation
    `maxcloud.gronblom.dev/registration`; run-demo's is in git), and the
    maxcloud controller only gives an app name's data to the registration
    it belongs to. A new entry under an old name starts empty, with
    `RegistrationMismatch` on its Buckets and Database saying why. To hand
    the old data over, as an admin, with the old ID from that message:

    ```fish
    kubectl annotate resourcesetinputprovider -n flux-system <app> maxcloud.gronblom.dev/adopt-registration=<old ID>
    ```

    Or delete the old buckets for a fresh start. Why and how:
    `docs/kb/data/maxcloud-app-owners-and-registrations.md`.
  - **A pushed `maxcloud.yaml` applies itself** (GitOps apps): the app's
    GitRepository includes it, and the maxcloud controller reads it from
    Flux's artifact and updates the entry's quota, buckets, database and
    domains.
    It writes as the user `maxcloud:app-sync`, so the developer limits in
    `infrastructure/configs/run/registry-admission.yaml` apply; a refusal
    lands in the entry's annotation `maxcloud.gronblom.dev/sync-error`.
    Network access and RBAC: `infrastructure/controllers/maxcloud/app-sync.yaml`.
  - **A rebuilt cluster gets them back:** the maxcloud controller copies
    every such entry (label `app.kubernetes.io/managed-by: mcloud`) to the
    bucket `maxcloud-registry` and recreates missing ones. A finalizer
    removes the copy when an app is deleted on purpose.
  - **The platform's own apps** (`run-demo`) stay in `apps/maxcloud/apps/`.
  - Removing an entry deletes the app, namespace and data included (buckets
    and database backups in Object Storage are kept).
- **App namespaces** (generated by the registry, plus
  `infrastructure/configs/run`):
  - Pod Security `restricted`.
  - A quota: 200m CPU and 1Gi memory requested, 20 pods, no LoadBalancers.
  - Default requests of 10m CPU and 64Mi memory.
  - NetworkPolicies: ingress only from the app itself, Traefik, Knative,
    Prometheus, CNPG and KEDA.
  - Egress: no connections to the platform's pods (Loki, Alertmanager,
    zot, Authelia, the maxcloud controller, the Knative activator, ...),
    only to other apps, Traefik, CoreDNS, the API server and the internet;
    nor to UpCloud's metadata service. A Cilium deny rule, so an app's own
    NetworkPolicies can't widen it
    (`infrastructure/configs/run/tenant-egress.yaml`). The activator
    serves any app's revision to whoever names it in a header, so only
    Traefik calls it.
  - Traefik Middlewares: forwardAuth only to Authelia, no `errors` or
    `plugin`, so an app can't send Traefik to platform services instead,
    and `headers` can't set `Host` or `Knative-Serving-*`
    (`infrastructure/configs/run/tenant-routing.yaml`). The same file keeps
    `--` out of Knative service names and tags, which keeps
    `<service>--<app>` hostnames unique.
  - Routes: a `URLRewrite` may only rewrite the Host to one of the app's
    own `<service>.<app>.svc.cluster.local` names, a header filter can't
    set `Host`, and `Knative-Serving-Namespace` only to the app itself
    (`tenant-routing.yaml`). Traefik strips `Knative-Serving-*` from every
    incoming request; Knative's routes set them again. So neither an app's
    route nor a client can point Traefik or the activator at another app.
  - Which hostnames an app's pods may ask Traefik's in-cluster entrypoint
    (8081) for: a Cilium L7 rule on Traefik's side, one policy per app
    plus `infrastructure/configs/run/traefik-ingress.yaml`
    (`private/docs/kb/networking/traefik-ingress-host-rules.md`).
  - The app repo is applied as a ServiceAccount with the built-in `admin`
    role in that namespace only, which can't change the quota.
  - `admin` would also let it make Flux objects and certificates from the
    platform's ClusterIssuers (any name in its zones). An admission policy
    refuses both (`infrastructure/configs/run/tenant-kinds.yaml`). Behind
    it, Flux and the Flux Operator run in multitenant mode: a Flux object
    that names no service account applies as its namespace's `default` (or
    `flux-operator`) SA, which has no rights, and can't use a source in
    another namespace
    ([multitenant-lockdown](docs/kb/flux/multitenant-lockdown.md)).
  - Every pod must use the `tenant` PriorityClass (-100), which an admission
    policy enforces. When one node holds everything during a drain and runs
    out of room, app pods are the ones evicted or left Pending.
- **Source deploys:** `run-deploy`, a reusable workflow in the public
  [maxcloud](https://github.com/maxemiliang/maxcloud) repo (public, so that
  public app repos can call it too).
  - All its work is `mcloud build --ci`, the same build mcloud's direct
    deploys run: the app's Dockerfile, or else Paketo buildpacks (non-root
    images).
  - It pushes to the maxcloud registry, `registry.gronblom.dev/<app>/<app>`,
    logged in as the repo itself (GitHub Actions' OIDC token, with mcloud as
    docker's credential helper: the app repo's deploy workflow grants
    `id-token: write`), and commits the digest to the app repo's
    `deploy/k8s`. There's no GHCR fallback any more.
- **Image registry:** zot at `registry.gronblom.dev`
  (`infrastructure/controllers/registry`), maxcloud's own registry, so
  apps' images don't depend on a GitHub account's packages.
  - Images are in the `maxcloud-apps` Object Storage (bucket
    `maxcloud-images`, its own S3 user `zot`), so they outlive the cluster.
    zot's metadata needs no disk: every start rebuilds it from the bucket.
  - DNS-only: Cloudflare's free proxy caps request bodies at 100 MB. The
    `websecure` entrypoint allows 15 minutes to send a request (Traefik's
    default is 60s, too short for a large layer).
  - Logins are OIDC tokens, no passwords:
    - People: Authelia's ID tokens for the client `kubernetes`
      (`docker login registry.gronblom.dev -u zot` with the token from
      `mcloud credential` as the password). Authelia's `admins` may do
      everything.
    - Apps: each app namespace pulls `registry.gronblom.dev/<app>/...`,
      and nothing else, with a token of its ServiceAccount
      `maxcloud-image-pull` that the maxcloud controller keeps in the Secret
      `maxcloud-registry` (renewed at half-life) and adds to the `default`
      ServiceAccount's `imagePullSecrets`. zot checks these tokens against
      the cluster's own issuer, which is why anyone who can reach the API
      server may read its public signing keys
      (`infrastructure/controllers/registry/issuer-discovery.yaml`).
    - Pushes to `<app>/...`: the app's repo's GitHub Actions (their OIDC
      token, audience `registry.gronblom.dev`), and mcloud for direct apps
      (a token of the app's ServiceAccount `maxcloud-image-push`, which
      only the app's owners and admins can get).
    - Nothing anonymous.
  - Who may do what per app comes from the app registry: the maxcloud
    controller writes zot's configuration (the ConfigMap `zot-config`) from
    `config.json` here (the ConfigMap `zot-base`) plus one rule per app,
    and restarts zot when the base changes. Edit `config.json`, never
    `zot-config`.
  - Garbage collection daily (05:00-08:00 UTC). Retention keeps each
    repository's 10 most recently pushed tags and anything pushed in the
    last 90 days (pull times don't survive a restart, so no rule uses
    them). Untagged manifests (e.g. replaced build caches) go after a day.
- **Event-driven jobs:** every event becomes a row in a queue table in the
  app's own Postgres. KEDA (`infrastructure/controllers/keda`) polls the
  backlog with a count query and starts work.
  - A ScaledJob starts one Job per pending row.
  - A ScaledObject scales a worker Deployment from 0 to N.
  - Events reach the table in different ways:
    - Webhooks: the app's Knative service checks the request, inserts a row
      and returns.
    - Uploads: the app hands out presigned URLs and inserts a row when the
      upload is confirmed.
    - Schedules: a CronJob.
  - The queue's database is the registry's `db` (Databases, below). Examples
    are in `templates/run-app/deploy/examples` (`queue-job.yaml`,
    `queue-worker.yaml`).
  - `apps/run-demo/queue.yaml` runs the whole chain, with PostgREST as the
    webhook receiver. Its header has the command to try it.
- **Buckets (like R2):** the registry field `buckets`
  (`buckets: [uploads, assets:public]` in the app's `maxcloud.yaml`) becomes Bucket
  objects in the app's namespace. The maxcloud controller
  (`clusters/personal/maxcloud.yaml`, from the maxcloud repo at a release
  tag) turns them into buckets `mc-<app>--<bucket>` in the `maxcloud-apps`
  Object Storage, with one S3 user per app that can only use its own
  buckets.
  - Every function gets the credentials from the Secret `maxcloud-s3` as
    `AWS_*` environment variables, plus `MAXCLOUD_APP` and each bucket's
    full name as `MAXCLOUD_BUCKET_<NAME>` (`MAXCLOUD_BUCKET_UPLOADS`), for
    frameworks configured by environment. A pod started before a bucket
    existed doesn't have its variable; `mc-<app>--<bucket>` is the rule.
  - `kubectl get maxcloud -A` shows the buckets with their sizes (plain
    `kubectl get buckets` means Flux's own Bucket kind); the
    controller exports them as metrics, with alerts for reconcile errors,
    the controller being down, its token expiring, and storage nearing the
    next 250 GB billing step.
  - Dropping a bucket from the list keeps it and its objects (still
    billed); delete those by hand.
  - **Public buckets** (`assets:public`): anyone can read (not list) the
    objects. The controller sets a public-read bucket policy and serves
    the bucket at `https://<bucket>--<app>.gronblom.dev/` (GET and HEAD,
    proxied and cached by Cloudflare) through Traefik: HTTPRoute,
    TraefikService and an ExternalName Service in `maxcloud-system`, since
    the Gateway API can't route outside the cluster
    (`MAXCLOUD_PUBLIC_DOMAIN` and `MAXCLOUD_PUBLIC_GATEWAY` in
    `infrastructure/controllers/maxcloud/config.yaml`; details in the
    controller's README). Removing `:public` takes both away again.
- **Databases (like D1, but Postgres):** the registry field `db`
  (`db: true` in the app's `maxcloud.yaml`, or `db: {size: 2Gi,
  instances: 2}`) becomes a Database `db`, which the maxcloud controller
  runs as a CloudNativePG Cluster `db` (every function gets `DATABASE_URL`
  from `db-app`).
  - It archives WAL and takes base backups into the app's bucket
    `mc-<app>--db`, in a folder per *generation* (`db-1`, `db-2`, ...). The
    marker `maxcloud.json` in that bucket says which generation is current.
  - When the Cluster is gone (deleted, the namespace lost, a rebuilt
    cluster), the controller restores the current generation into the next
    one by itself. When it can't account for what's in the bucket it stops
    (alert `MaxcloudDatabaseBlocked`) and a person picks a generation:
    `kubectl -n <app> annotate database db maxcloud.gronblom.dev/recover-from=<n>`.
    The rules are in the maxcloud repo, `controller/README.md`.
  - Its backups are watched by the `homelab.cnpg` alerts like any other
    CloudNativePG database. Removing `db` deletes the database and keeps
    its backups.
- **Monitoring:**
  - Grafana dashboard "Run platform": requests, 5xx, p95 latency and pods per
    Knative service, plus queue backlog and running Jobs per app.
  - Alerts `KnativeRevisionPodsPending`, `KnativeServiceErrors` and
    `KedaScalerErrors` (KEDA can't read a queue). Failed Jobs are
    `KubeJobFailed`.
  - A service that fails to deploy shows up as `FluxResourceNotReady`.
- **Workers and cron jobs** are plain Deployments and CronJobs in the same
  namespace (`templates/run-app/deploy/examples`).

## Procedures

- [Restore a Postgres cluster from backup](docs/runbooks/postgres-restore.md).
- [Move an app's database.yaml database to the registry's `db`](docs/runbooks/move-database-to-registry.md).
- **Add an app from its own repo:** put it on the run platform (`mcloud
  deploy` in the app's repo, see Run platform). The registry makes its
  namespace, and Flux applies the repo as a ServiceAccount that is admin in
  that namespace only. A custom hostname needs an entry in
  `infrastructure/configs/run/domains.yaml` first.
- **Add a platform HelmRelease or Flux Kustomization:** name the identity
  Flux applies it as. A HelmRelease gets `serviceAccountName: flux-helm`,
  and a new namespace gets its `flux-helm` SA in
  `infrastructure/controllers/flux-helm.yaml`; a Kustomization in
  flux-system gets `serviceAccountName: kustomize-controller`. Without it,
  it fails with `system:serviceaccount:<ns>:default cannot ...` (Flux is in
  multitenant mode; rollback in
  [multitenant-lockdown](docs/kb/flux/multitenant-lockdown.md)).
- **Write the image registry's S3 credentials** (after `tofu apply` in
  `infra/storage`, or after rotating the `zot` user's access key). In fish,
  from `platform/`, with the `private/` submodule checked out; then commit
  and push it in `private/`:

  ```fish
  set out ../private/secrets/registry/zot-s3.sops.yaml
  printf '%s\n' \
    "AWS_ACCESS_KEY_ID="(tofu -chdir=infra/storage output -raw zot_access_key_id) \
    "AWS_SECRET_ACCESS_KEY="(tofu -chdir=infra/storage output -raw zot_secret_access_key) \
    | kubectl -n registry create secret generic zot-s3 \
        --from-env-file=/dev/stdin --dry-run=client -o yaml \
    | sops encrypt --filename-override $out > $out
  ```
- **Run an app on the run platform** with
  [mcloud](https://github.com/maxemiliang/maxcloud):
  1. In the app's repo, `mcloud init` (a functions app in TypeScript, or
     `--lang go`), or copy `templates/run-app` for any other app. Its
     `maxcloud.yaml` describes the app: quota, buckets, database, custom
     hostnames.
  2. Push it, and let its first deploy run commit an image digest.
  3. `mcloud deploy`: registers the app in the cluster from `maxcloud.yaml`
     the first time (nothing to commit here), with you as its owner, then
     follows the deploy.
  4. Give Flux's GitHub App (`maxcloud-deployment`) access to the repo
     (GitOps apps). If it hasn't got it, `mcloud deploy` says so, opens
     https://github.com/apps/maxcloud-deployment/installations/new, and
     waits until Flux can read the repo.
  5. `mcloud deploy` from then on, or just push. A pushed change to
     `maxcloud.yaml` reaches the cluster by itself (GitOps apps; direct
     ones with the next `mcloud deploy`).

- **Log in to the cluster through Authelia** (mcloud and kubectl, no admin
  kubeconfig). The cluster's API server trusts Authelia's ID tokens for
  the client `kubernetes` (UKS cluster authentication, UpCloud API beta;
  `infra/cluster-auth/authentication.yaml`). Authelia's `admins` group is
  cluster-admin, and `maxcloud-developers` may register apps and run the
  ones they own (`infrastructure/configs/auth`). The API server only
  accepts connections from `admin_cidrs` (`TF_VAR_admin_cidrs`,
  `infra/cluster/cluster.tf`) and the NAT gateway, so a developer's address
  has to be in that list too. Once, and again after a cluster rebuild:

  ```fish
  read -s -P 'UpCloud API token (homelab_api): ' -x UPCLOUD_TOKEN
  scripts/cluster-auth.sh apply     # takes up to a minute
  set -e UPCLOUD_TOKEN
  ```

  Then, per machine: `mcloud login`. It takes the cluster's address and CA
  from the discovery document (`https://maxcloud.gronblom.dev/.well-known/
  maxcloud.json`, the maxcloud controller serves it:
  `infrastructure/controllers/maxcloud/discovery.yaml`), writes mcloud's
  kubeconfig (`~/.config/mcloud/kubeconfig`; kubectl can use it too), and
  mcloud uses it from then on. `mcloud doctor` checks the setup.
  **Break-glass** (Authelia or the cluster down, so no OIDC login):
  `upctl kubernetes config` writes a fresh admin kubeconfig with the
  `homelab_api` credentials.
- **Renew the maxcloud controller's UpCloud token** (alert
  `MaxcloudTokenExpiringSoon`; tokens last at most 365 days). In fish, from
  `platform/` (the `private/` submodule checked out), with the `maxcloud_controller` sub-account's password from the
  password manager:

  ```fish
  read -s -P 'maxcloud-controller password: ' -x SUB_PW
  set NAT_IP (tofu -chdir=infra/cluster output -raw nat_egress_ip)
  set out ../private/secrets/maxcloud-system/controller-credentials.sops.yaml
  set EXPIRES (date -u -d '+364 days' +%Y-%m-%dT%H:%M:%SZ)
  set TOKEN (env UPCLOUD_USERNAME=maxcloud_controller UPCLOUD_PASSWORD=$SUB_PW \
    upctl account token create --name maxcloud-controller-cluster \
    --expires-at $EXPIRES --allow-ip-range $NAT_IP/32 -o json | jq -r .token)
  # Keep the apps' secrets backup key (below) as it is.
  set AGE_KEY (sops decrypt --extract '["data"]["MAXCLOUD_SECRETS_AGE_KEY"]' $out | base64 -d)
  printf '%s\n' "UPCLOUD_TOKEN=$TOKEN" "UPCLOUD_TOKEN_EXPIRES_AT=$EXPIRES" \
    "AWS_ACCESS_KEY_ID="(tofu -chdir=infra/storage output -raw maxcloud_controller_access_key_id) \
    "AWS_SECRET_ACCESS_KEY="(tofu -chdir=infra/storage output -raw maxcloud_controller_secret_access_key) \
    "MAXCLOUD_SECRETS_AGE_KEY=$AGE_KEY" \
    | kubectl -n maxcloud-system create secret generic maxcloud-controller \
        --from-env-file=/dev/stdin --dry-run=client -o yaml \
    | sops encrypt --filename-override $out > $out
  set -e TOKEN SUB_PW AGE_KEY
  ```

  Commit and push in `private/`; then restart the controller
  (`kubectl -n maxcloud-system rollout restart deploy/maxcloud-controller`)
  and delete the old token (`upctl account token list` /
  `upctl account token delete <id>`, as the sub-account).
- **Give the maxcloud controller its secrets backup key** (once; apps'
  secrets, `mcloud secrets set`, live in their namespaces, and the
  controller keeps copies in the `maxcloud-registry` bucket encrypted to this
  key, which it uses to put them back after a rebuild). In fish, from
  `platform/` (the `private/` submodule checked out):

  ```fish
  set out ../private/secrets/maxcloud-system/controller-credentials.sops.yaml
  set -x AGE_KEY (age-keygen 2>/dev/null | string match 'AGE-SECRET-KEY-*')
  sops decrypt $out | yq '.data.MAXCLOUD_SECRETS_AGE_KEY = (strenv(AGE_KEY) | @base64)' \
    | sops encrypt --filename-override $out > $out.new; and mv $out.new $out
  set -e AGE_KEY
  ```

  Commit and push in `private/`; then restart the controller
  (`kubectl -n maxcloud-system rollout restart deploy/maxcloud-controller`).
  Losing the key loses only the copies: the controller makes new ones with a
  new key.
- **Images from outside the maxcloud registry** (e.g. GHCR): the cluster
  pulls them without credentials, so the package must be public. A pull
  secret can't come from the app's repo any more: Flux doesn't decrypt app
  repos ([app-kustomizations-no-sops](docs/kb/build-deploy/app-kustomizations-no-sops.md)).
  Images in the maxcloud registry need nothing.
- **Raise an app's quota:** `quota:` in the app's `maxcloud.yaml`, up to
  1 CPU, 4Gi memory and 40 pods (a database: 10Gi, 2 instances). That's
  the ceiling for developers in `infrastructure/configs/run/registry-admission.yaml`,
  and app sync is held to it too. Above it, an admin sets the value on the
  entry (`mcloud apps set <app> --cpu 2`), and the app's `maxcloud.yaml`
  must leave `quota:` out. While a value is above the ceiling, every change
  to the entry by its owners or by app sync is refused. A platform app
  kept in git (run-demo): `cpu`, `memory` or `pods` on its entry in
  `apps/maxcloud/apps/`.
- **Give an app a custom hostname** (e.g. `x.lankkupelit.fi`): add it to
  the app's key in `infrastructure/configs/run/domains.yaml` and push. The
  app then lists it in `domains:` in its `maxcloud.yaml`. A hostname two
  levels below a zone (`a.b.lankkupelit.fi`) needs a listener and
  certificate of its own first. Taking one off the list takes its route
  down within 5 minutes.
- **Authelia credentials:** `scripts/authelia-secrets.sh` regenerates every
  Authelia secret and the user. Afterwards, register passkeys/TOTP again; the
  one-time code is in
  `kubectl -n authelia exec deploy/authelia -- cat /data/notification.txt`.
- **Grafana when Authelia is down:** set `auth.disable_login_form` and
  `auth.basic.enabled` back to their defaults in
  `infrastructure/controllers/monitoring/kube-prometheus-stack.yaml`, push, and
  log in as `admin` (password in `grafana-admin.sops.yaml`).
- **A deploy is stuck:** `flux get all -A --status-selector ready=false`. A new
  commit interrupts a Kustomization that is waiting on health checks, so pushing
  a fix is enough.

## Known gaps

- **A node reboot drops new connections for a few seconds.** Traefik runs as a
  DaemonSet, which a drain doesn't move, so the LB keeps sending connections to
  a rebooting node until its health check (every 2s, 2 failures) takes it out.
  Measured: about 5s, only inside kured's 01:00-04:00 UTC window.
- **No DR drill yet.** Rebuilding from scratch has never been tested end to end:
  destroy `infra/cluster`, apply `infra/cluster` and `infra/flux`, then check that
  Flux restores everything. Until then, only the single-database restore runbook
  is proven. Known so far: Authelia's passkeys/TOTP live on a cluster volume
  and must be registered again. Run platform databases restore themselves from
  their buckets (the maxcloud controller), which a drill should confirm.

- **No bucket events.** UpCloud Object Storage documents no bucket
  notifications, so nothing can react to a file landing in a bucket. Uploads
  go through the app (presigned URL, then a queue row). For a bucket that
  others write to, a CronJob would have to list it.
- **A `database.yaml` database has no backups.** The template's, and the one
  mcloud wrote for queue functions before v0.5.0, run without them. Use the
  registry's `db` field instead (Run platform, Databases), which backs up
  and restores itself; an app can't have both, since both are Cluster `db`.
  Moving one over: [docs/runbooks/move-database-to-registry.md](docs/runbooks/move-database-to-registry.md).
- **Traefik isn't on net-gateway-api's tested list** (Istio, Contour, Envoy
  Gateway). Verified on a local kind cluster with Traefik 3.7.16 and Knative
  1.24, 2026-11-04:
  - public and private routing
  - readiness probes
  - 50/50 traffic split and tag URLs
  - scale to zero, with about 0.75s cold start
  - SSO route
  - quota, priority and network policies

  Recheck after Traefik or Knative upgrades.
- **Traefik can't use Knative's ExternalName Services as backends**, so SSO
  routes point at `traefik/traefik-knative` with a host rewrite and a
  ReferenceGrant (generated per app by `apps/maxcloud/resourceset.yaml`).

## Costs and capacity

About €63/month: 2× ~€24 nodes (STARTER-2xCPU-16GB), 2× €5 object storage
(`homelab`, and `maxcloud-apps` for apps' buckets and backups), and ~€5
for 21 GiB of MaxIOPS block storage (Prometheus 10, Loki 5, Postgres 2×2,
Alertmanager 1, Authelia 1). The control plane (`dev-md`), NAT gateway and load
balancer (essentials) are free.

During a reboot or node replacement, one node runs everything, so the usable
capacity is one node's allocatable memory (~15 GiB). Measured 2026-11-01 on the
8 GB nodes: about 5.5 GiB in use when one node runs everything. Check
`kubectl top nodes` before adding a big workload.

On one node, the tighter limit is CPU *requests*, not memory: 2 cores
allocatable. Measured 2026-11-04 with both nodes' pods counted on one node:

| | CPU requested | Memory requested |
|---|---|---|
| Platform before the run platform | 1.32 cores | 3.9 GiB |
| Knative Serving and its operator, trimmed | ~0.17 cores | ~0.55 GiB |
| KEDA, trimmed | 0.03 cores | ~0.14 GiB |

Knative and its operator use about 220 MiB of memory in practice, and KEDA
about 100 MiB. That leaves
about 0.5 cores of requests for apps. Each scaled-up Knative pod requests 15m
by default (10m app, 5m queue-proxy), and an idle service requests nothing.

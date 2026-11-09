# run-app template

A starting point for an app on the homelab's run platform (Knative on the
homelab cluster) that isn't an mcloud functions app: any HTTP service with a
Dockerfile or a buildpacks-supported language. Copy this directory into a
new repo, or make the new repo from a GitHub template repo created from it.
Then follow the homelab README, Procedures, "Run an app on the run
platform": set `app:` in `maxcloud.yaml` and run `mcloud deploy` once to
register it.

## maxcloud.yaml

The app's definition on the platform: its name, and optionally its quota,
buckets, database and custom hostnames (`domains:`). The full list of
fields is in maxcloud's
[docs/maxcloud-yaml.md](https://github.com/maxemiliang/maxcloud/blob/main/docs/maxcloud-yaml.md).
`mcloud deploy` registers the app from it the first time; after that, a
pushed change to it applies itself. The owners (who may run the app) aren't
in the file: whoever registers it owns it, and `mcloud apps owners` adds
others.

## How it deploys

Push to `main`, and `.github/workflows/deploy.yaml` (maxcloud's
`run-deploy` workflow, in maxemiliang/maxcloud) runs:

1. It builds the repo: with its `Dockerfile` if there is one, otherwise with
   Cloud Native Buildpacks (Go, Node.js, Python, Java, .NET, Ruby, PHP).
2. It pushes the image to the maxcloud registry,
   `registry.gronblom.dev/<app>/<app>` (`app:` from `maxcloud.yaml`), as the
   repo itself (the workflow grants `id-token: write`).
3. It pins the image digest in `deploy/k8s/kustomization.yaml` and commits.

Flux syncs `deploy/k8s/` from `main` into the app's namespace within a minute.
A change to `deploy/` only (scaling, env vars) is applied without a build.

## What goes where

- `deploy/k8s/service.yaml`: the HTTP service. The app must listen on `$PORT`
  (8080).
  - It is private unless you add the label `run.gronblom.dev/expose: public`.
    Private means cluster-local: `http://web.<namespace>.svc.cluster.local`.
  - Public means `https://web--<namespace>.gronblom.dev`.
- `deploy/examples/`: copy into `deploy/k8s/` and list in its
  `kustomization.yaml` as needed.
  - `worker.yaml`: a background process with no HTTP.
  - `cronjob.yaml`: a scheduled job.
  - `sso.yaml`: the private service on `https://<name>.lab.gronblom.dev` behind
    the Authelia login. Needs an admin to allow the hostname first: the app
    sees admins' Authelia sessions. Hostnames for the public are `domains:`
    in `maxcloud.yaml` instead.
  - `database.yaml`: a Postgres database you run yourself (CloudNativePG),
    without backups. Prefer `db: true` in `maxcloud.yaml`: the same Cluster
    `db` and Secret `db-app`, backed up and restored by the maxcloud
    controller. An app can't have both.
  - `queue-job.yaml`: one Job per pending row in the `jobs` table, started by
    KEDA. Nothing runs while the queue is empty.
  - `queue-worker.yaml`: a worker that KEDA scales from 0 to 3 pods with the
    backlog.
  - The two queue examples need the app's database and the KEDA
    TriggerAuthentication `db`. With `db: true`, copy only the
    TriggerAuthentication from the end of `database.yaml`.

## Secrets and settings

`mcloud secrets set NAME` stores a secret in the Secret `app-secrets` in the
app's namespace; nothing goes in git (Flux doesn't decrypt SOPS files in
app repos). Point the app at it, e.g. `envFrom: [{secretRef: {name:
app-secrets, optional: true}}]` in `service.yaml`. Pods read it when they
start, so restart them after a change. Plain settings go straight into the
manifests as `env:`. More: maxcloud's
[docs/secrets.md](https://github.com/maxemiliang/maxcloud/blob/main/docs/secrets.md).

## Event-driven work

Every event becomes a row in the app's `jobs` table; KEDA starts the work.

- **Webhook:** the web service checks the request (signature, token),
  inserts a row and answers 200 right away.
- **Upload:** the web service hands out a presigned upload URL for the bucket
  and inserts a row when the client confirms. UpCloud Object Storage has no
  bucket notifications.
- **Schedule:** a CronJob inserts a row, or does the work itself.

Claim rows with `SELECT ... FOR UPDATE SKIP LOCKED`, so parallel workers
never take the same one. A library does this for you: River (Go), pg-boss
(Node.js), Procrastinate (Python). KEDA only needs a query that returns the
number of pending jobs.

## Shutting down

Pods are stopped all the time: on every scale to zero, every deploy and
every node drain. The app gets SIGTERM and should catch it, finish what it's
doing, and exit 0.

- **Web service:** Knative's queue-proxy sidecar finishes the in-flight
  requests (about 30 seconds) before the app gets SIGTERM. Stop the HTTP
  server cleanly, e.g. `server.Shutdown(ctx)` in Go or `server.close()` in
  Node.js.
- **Worker or job:** stop taking new jobs and finish or release the one in
  hand within `terminationGracePeriodSeconds` (60 in the worker example).

An app that ignores SIGTERM still stops, but with a non-zero exit code. The
app runs as PID 1 in its container, and for PID 1 the kernel ignores signals
the process doesn't handle; Go, for example, then exits with code 2. The pod
then shows `Error` in `kubectl get pods` for a moment before it's removed.
That's harmless for a web service but noisy, and a worker that dies mid-job
leaves that job to be retried.

## Rules the namespace enforces

- Every pod sets `priorityClassName: tenant` (already in the examples), or it
  is rejected.
- Containers run as non-root, with no privilege escalation and no extra
  capabilities (Pod Security `restricted`). Knative services get these
  settings automatically; Deployments and CronJobs spell them out.
- A quota caps the namespace at 200m CPU and 1Gi memory requested, and 20
  pods. Raise it with `quota:` in `maxcloud.yaml`, up to 1 CPU, 4Gi and 40
  pods; more takes an admin.
- Containers without resources get 10m CPU and 64Mi memory requested, and a
  256Mi memory limit.

## Useful commands

```fish
kubectl -n <namespace> get ksvc                 # URL, ready, latest revision
kubectl -n <namespace> get revisions
kubectl -n <namespace> logs -l serving.knative.dev/service=web -c user-container
flux -n flux-system get kustomizations <app>    # did the last commit apply?
```

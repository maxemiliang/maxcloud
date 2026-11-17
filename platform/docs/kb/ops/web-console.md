---
title: The web console reads the cluster with each visitor's own token, as mcloud's login client, and has no rights of its own
kind: decision
area: ops
tags: [console, authelia, oidc, pkce, session, rbac, knative, secrets, sops, ghcr, maxcloud]
learned: 2026-11-17
updated: 2026-11-17
where:
  - apps/console/service.yaml
  - apps/maxcloud/apps/console.yaml
  - infrastructure/controllers/authelia/configuration.yml
  - infrastructure/configs/run/domains.yaml
  - ../console/server/lib/kube.ts
  - ../console/server/lib/session.ts
  - ../.github/workflows/console-image.yaml
  - ../docs/console.md
---

# The web console reads the cluster with each visitor's own token, as mcloud's login client, and has no rights of its own

**Decision:** `https://console.makiya.dev` is a Nuxt app (the repo's
`console/`) running as the platform's own app `console`, an entry kept in
git like run-demo. Its server logs people in through Authelia (the
authorization code flow with PKCE) as the public client `kubernetes`, the
one mcloud uses, keeps the ID token and refresh token in a sealed cookie,
and calls `https://kubernetes.default.svc` with that ID token as the
bearer. The pod mounts no ServiceAccount token
(`automountServiceAccountToken: false`; the CA comes from the ConfigMap
`kube-root-ca.crt`).

**Why:**

- **The API server stays the only place that decides who sees what.** The
  console shows exactly what mcloud would show the same person, and a bug
  in it can't show more: there's no credential in the pod to show it with.
- **The client `kubernetes` because its ID tokens are the ones the API
  server accepts** (audience `kubernetes`). Nothing in the cluster's
  authentication changes; the client gained one redirect URI. It's a
  public client, so there's no secret to keep either.
- **No token in the browser.** The server holds them; the cookie is sealed
  (AES-256-GCM), `HttpOnly`, `Secure`, `SameSite=Lax`, `__Host-`.
- **`console.makiya.dev`, a custom domain on a private service:** one
  hostname, so the redirect URI and the cookie have one origin, and never
  under `lab.gronblom.dev`, where the app's pods would get Authelia's own
  session cookie. Not proxied by Cloudflare: its pages and cookie stay
  between the browser and the cluster.
- **One instance (`max-scale: "1"`).** Authelia's refresh tokens work
  once. A page's requests arrive together with the same cookie, so the
  server refreshes once and gives all of them the result, and keeps it for
  a minute for stragglers. That memory is one process's. A second instance
  would cost the unlucky request its login, nothing worse.
- **Authelia and the apps API at their public hostnames:** an app's pods
  can't connect to platform pods
  ([tenant-egress-deny](../networking/tenant-egress-deny.md)).

**Rejected:**

- **A ServiceAccount that may read apps, with the console checking who
  owns what:** a second copy of the rules RBAC already holds, and every
  bug in it is a leak.
- **The cookie-sealing key as a SOPS Secret in the private repo,** like
  other platform secrets. The `secrets` Kustomization applies all its
  Secrets or none, and fails while any namespace is missing. The namespace
  `console` comes from the registry (`apps`), which waits for
  infra-configs, which waits for infra-controllers, whose pods need those
  Secrets: on a rebuilt cluster nothing would ever start. So it's an app
  secret (`mcloud secrets set CONSOLE_SESSION_KEY --app console`, in the
  Secret `app-secrets`), which the controller copies and puts back. Losing
  it only logs everyone out.
- **`@max-cloud/nuxt` building it as a function:** that build pushes to the
  maxcloud registry as the app's GitHub repo and commits the digest to the
  app repo's `deploy/k8s`. A platform app has no repo in its entry, and a
  workflow committing to this repo's `main` would be a deploy nobody
  reviewed. A plain Knative Service and a public GHCR image, built by
  `console-image.yaml` and bumped by a Renovate PR, is what the sandbox
  image already does. The module's other half (runtimeConfig from the
  platform) has nothing to map: the console has no database or buckets.
- **Sessions in a database** (`db: true`): a Postgres that never scales to
  zero, for a few cookies. It would allow real log-out and more than one
  instance; see Revisit.
- **A forward-auth route on `lab.gronblom.dev`** (as run-demo's whoami):
  Authelia's cookie would reach the console's pods, and forward auth gives
  headers, not an ID token the API server takes.
- **Its own Authelia client:** the API server would have to trust a second
  audience (`infra/cluster-auth`), for no gain over one more redirect URI.

**Verified** (2026-11-17, without the live cluster): on kind with Knative
1.24.0, Pod Security `restricted` and `secure-pod-defaults`, the Service
is accepted as written and its pod runs. Without the Secret the pod waits
with `CreateContainerConfigError: secret "app-secrets" not found` and
starts by itself once it exists. In the pod there is no
`/var/run/secrets/kubernetes.io/serviceaccount`, and
`kubernetes.default.svc` verifies with the mounted CA and not without it.
Authelia's live discovery document passes the console's checks. Today the
apps API's host answers `404 page not found` for `/api/v1/apps/...`, which
the console shows as "the platform doesn't serve apps' logs yet".

**Not verified, needs the live cluster:**

- A pod in an app namespace reaching `auth.lab.gronblom.dev` and
  `maxcloud-api.gronblom.dev` (the API gateway, where the apps API is
  served). Grafana already calls Authelia's token endpoint
  at that hostname from `monitoring`, and apps may reach the internet and
  Traefik, so both ways in are allowed; nobody has watched it from a
  tenant pod. If it fails, logging in ends at `/login?error=provider` and
  the pod logs `the login provider can't be reached`.
- Authelia's side of the browser login for this redirect URI (it's the
  flow `mcloud login --browser` uses), and whether it asks for consent on
  every login.
- That a real ID token is accepted on `kubernetes.default.svc`. It's the
  same API server that takes it from outside, and the gateway already
  passes people's tokens to it from inside.

**Known limits:**

- Logging out clears the cookie and revokes the refresh token; an ID token
  already issued works until it expires (an hour). A copied cookie lives
  that long.
- The apps API's host is behind Cloudflare, so the ID token passes through
  it on those calls, as mcloud's does
  ([apps-observability-api](../monitoring/apps-observability-api.md)).
- A developer can register an app named `console` before this entry is
  applied; nothing reserves the name.

**Revisit when:** the console should change things (then: a CSRF token per
form, and think again about what a stolen session can do), when it needs
two instances or real log-out (a session store), or when deploy history
from GitHub is wanted (a GitHub login for the console).

See also: [api-gateway](api-gateway.md),
[maxcloud-app-owners-and-registrations](../data/maxcloud-app-owners-and-registrations.md).

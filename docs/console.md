# The web console

`https://console.makiya.dev` shows your apps in a browser: what `mcloud
apps list` and `mcloud status` show, plus logs, metrics, alerts and past
revisions. It's read-only. Deploying, secrets, databases and everything
else that changes something stay in [mcloud](mcloud.md).

## Logging in

"Log in with Authelia" takes you to the same login `mcloud login` uses:
your username and password, then your passkey or authenticator code. The
console then acts as you. It reads the cluster with your login and has no
rights of its own, so it shows what mcloud would show you and nothing
more.

A login lasts 12 hours, like mcloud's. "Log out" ends it in this browser;
Authelia and mcloud keep their own logins.

## What you see

- **Apps:** every registered app, its owners, whether its last deploy
  applied, and how many of its services are ready. An app's environments
  sit under it.
- **One app:** the page is `mcloud status`: source, owners, deploy, quota,
  then services with their URLs, workloads, the database, buckets, custom
  domains and problems (pods that aren't well, warnings from the last 15
  minutes). "follow" reads it again every 15 seconds, for watching a
  deploy.
- **Alerts, metrics, logs:** what `mcloud alerts`, `mcloud metrics` and
  `mcloud logs --since` show, from the platform. Logs go back 7 days here
  (mcloud: 30), 1000 lines at most, with a filter over the lines shown.
- **Revisions:** the revisions Knative keeps for each service, with their
  image and which one has the traffic. That's the history the cluster
  itself has. `mcloud history` reads a GitOps app's deploys from GitHub,
  which the console has no login for.

You see all of an app you own. For an app you don't own you see that it
exists, its services, database and buckets, and a note saying that its
pods, workloads, quota, domains, problems, alerts, metrics and logs are for
its owners and admins. A panel the platform can't answer (not offered yet,
or Loki is down) says so and leaves the rest of the page alone.

## How it works

The console is a Nuxt app in [`console/`](../console), running on maxcloud
as the platform's own app `console`
([`platform/apps/console`](../platform/apps/console)). Its server does all
the talking; the pages only ever ask the console itself.

- **Login:** OpenID Connect against Authelia, the authorization code flow
  with PKCE, as the client `kubernetes` that mcloud uses. That client's ID
  tokens are what the API server accepts, so nothing about the cluster's
  logins changed for the console. No client secret exists.
- **The session** is one cookie holding the ID token and the refresh
  token, sealed with AES-256-GCM (`CONSOLE_SESSION_KEY`), `HttpOnly`,
  `Secure`, `SameSite=Lax`, named `__Host-mc_session`. No token reaches a
  page or a script. The server refreshes the ID token when it has less
  than five minutes left. Refresh tokens work once, so requests that
  arrive together share one refresh, and one that arrives just after gets
  the same new tokens.
- **Reading the cluster:** `https://kubernetes.default.svc` with
  `Authorization: Bearer <your ID token>`. The pod mounts no
  ServiceAccount token. The server can make 20 requests, listed in
  [`server/lib/kube.ts`](../console/server/lib/kube.ts): 19 reads, and
  asking the API server who the token belongs to. The only thing that
  varies is an app's name, which must be a DNS label. No Secrets, no pod
  logs, no exec. It only shows apps' namespaces, whatever else an admin's
  login could read.
- **Logs, metrics, alerts** come from the platform's apps API
  (`https://maxcloud-api.gronblom.dev/api/v1/apps/<app>/...`), asked with your
  ID token, as mcloud does. The controller there decides who gets an
  answer.
- **Nothing changes anything.** Every route is a `GET` except logging in
  and out, which are form posts accepted only from the console's own
  origin. After logging in you land on a path of this site and nowhere
  else.
- **Pages** carry a Content-Security-Policy with a nonce per response:
  scripts and styles from the console only, nothing inline, no other
  hosts, no frames.

What it doesn't do: logging out can't take back an ID token that's already
issued (it lasts up to an hour). The cookie is gone and the refresh token
is revoked, but a copy of the cookie made before logging out works until
the ID token expires.

The same JSON is there for scripts that carry the session cookie:
`/api/apps` is `mcloud apps list --json` and `/api/apps/<app>` is `mcloud
status --json` ([the fields](mcloud.md#json-output)), each with a few
fields of the console's own (`health`, `services` in the list,
`restricted`, `environmentOf`, `observed`).
`alerts` there is always `null`: `/api/apps/<app>/alerts` has them.

## Working on it

```fish
cd console
npm ci
npm run dev                         # http://localhost:3000, made-up apps, no cluster
```

`npm run dev` is mock mode (`CONSOLE_MOCK=1`): a made-up cluster with the
platform's access rules, so logging in as `alice` (a developer who owns
`shop` and `notes`) shows other people's apps the way a developer sees
them, and `admin` sees everything. A failing app, one rolling out and one
with a refused `maxcloud.yaml` are in there too.

```fish
npm test                            # the server's libraries: session, login, status, the request allowlist
npm run typecheck
npm run build                       # the production server, in .output
npm run test:e2e                    # that server against a made-up Authelia, API server and apps API
npm run test:parity                 # its status against mcloud's own, for the same made-up cluster (needs Go)
```

`test:parity` builds `mcloud` from this repo and compares `mcloud status
--json` and `mcloud apps list --json` with the console's answers. Run it
after changing `internal/kube/status.go`, `internal/cli/output.go` or
[`console/server/lib/status.ts`](../console/server/lib/status.ts), which is
a port of the first two.

**Against the real cluster, from this machine.** The login client's one
local redirect URI is mcloud's, `http://127.0.0.1:18000/callback`, so the
console can use it too:

```fish
npm run build
set -x CONSOLE_ORIGIN http://127.0.0.1:18000
set -x CONSOLE_OIDC_REDIRECT_PATH /callback
set -x CONSOLE_SESSION_KEY (openssl rand -base64 32)
set -x CONSOLE_KUBE_URL https://maxcloud-api.gronblom.dev   # the API gateway
env HOST=127.0.0.1 PORT=18000 node .output/server/index.mjs
```

From an address the API server takes connections from, its own URL works
as well, with its CA in a file (both are in the discovery document,
`https://maxcloud.gronblom.dev/.well-known/maxcloud.json`):
`CONSOLE_KUBE_URL` and `CONSOLE_KUBE_CA_FILE`.

## Configuration

Environment variables, read at start. The server doesn't start without an
origin and a sealing key, with a URL that isn't `https` (plain `http` is
accepted for `localhost` and `127.0.0.1` only), or with a CA file it can't
read. It says which, and exits.

| Variable | Default | |
| --- | --- | --- |
| `CONSOLE_ORIGIN` | none: required | the console's own URL (`https://console.makiya.dev`): the login's redirect URI, and the only origin its forms are accepted from |
| `CONSOLE_SESSION_KEY` | none: required | the cookie-sealing key: 32 random bytes in base64 (`openssl rand -base64 32`). Several, comma-separated, to change it: the first seals, all of them open |
| `CONSOLE_OIDC_ISSUER` | `https://auth.lab.gronblom.dev` | the login provider |
| `CONSOLE_OIDC_CLIENT_ID` | `kubernetes` | its client: the one whose ID tokens the API server accepts |
| `CONSOLE_OIDC_REDIRECT_PATH` | `/auth/callback` | or `/callback`, for a local run |
| `CONSOLE_KUBE_URL` | `https://kubernetes.default.svc` | the API server |
| `CONSOLE_KUBE_CA_FILE` | the system's CAs | a PEM file with the CA that signed its certificate (the pod mounts the ConfigMap `kube-root-ca.crt`) |
| `CONSOLE_APPS_API` | `https://maxcloud-api.gronblom.dev/api/v1/apps/` | the platform's apps API |
| `CONSOLE_USER_PREFIX` | `authelia:` | what the API server puts before a person's username; other logins are turned away |
| `CONSOLE_MOCK` | off | `1`: mock mode. Never in the cluster |
| `HOST`, `PORT` | `0.0.0.0`, `3000` (the image: `8080`) | where it listens |

Running it on the platform, the sealing key and releasing a new version:
[the platform README](../platform/README.md#procedures), "The web
console".

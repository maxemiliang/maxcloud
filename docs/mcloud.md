# mcloud

mcloud is maxcloud's command line. Every command and flag:
[the reference](reference/cli/README.md).

## Installing

```fish
mise use -g github:maxemiliang/maxcloud   # or: go install github.com/maxemiliang/maxcloud/cmd/mcloud@latest
mcloud login                              # once per machine: log in, and set up cluster access
mcloud doctor                             # if anything's off: says what, and how to fix it
```

## Getting access

An admin adds you as a user, and gives you a username and a first
password. That's all they have to do: your address doesn't need to be on
any list, and there's no kubeconfig to pass around.

1. Open the login page `mcloud login` shows, and log in with the username
   and password.
2. It asks you to register a second factor (a passkey or an authenticator
   app). That takes a one-time code, which the admin reads out for you:
   ask them for it. With a passkey registered you can log in with it
   alone from then on.
3. `mcloud login` finishes, and `mcloud doctor` shows what's set up.

You can then register and run your own apps (`mcloud init`, `mcloud
deploy`), and see that other apps exist, but not their data. A GitOps
app's repo also needs the platform's GitHub App, which the first `mcloud
deploy` walks you through.

## Logging in

mcloud (and kubectl) log in to the cluster through the homelab's Authelia,
and act with what your groups allow. `mcloud login` is all the setup:

- it reads the platform's discovery document
  (`https://maxcloud.gronblom.dev/.well-known/maxcloud.json`,
  `$MCLOUD_PLATFORM_URL`): the cluster's address and CA, and the oldest
  mcloud that works with it (older ones warn);
- it logs in: a device code you approve in any browser (`--browser`: this
  machine's);
- it writes mcloud's kubeconfig, `~/.config/mcloud/kubeconfig` (on macOS
  `~/Library/Application Support/mcloud/kubeconfig`), which mcloud uses
  from then on. For kubectl: `set -gx KUBECONFIG
  ~/.config/mcloud/kubeconfig`.

The cluster's API server only takes connections from addresses an admin
has listed. From anywhere else, mcloud (and kubectl, with that kubeconfig)
goes through the platform's API gateway instead: the same login, the same
rights, a little slower. `mcloud login` tries the API server and writes
whichever this network needs, so after moving to another network (home,
then a café), run `mcloud login` again; `mcloud doctor` says when that's
what's wrong. `--via gateway` or `--via direct` (`$MCLOUD_VIA`) picks one
outright.

The kubeconfig runs `mcloud credential` for a token: the ID token lasts an
hour and is refreshed by itself; logging in again is needed after 12 hours
or `mcloud logout`. On a terminal, mcloud (and kubectl) then starts the
device login by itself. mcloud picks its kubeconfig from `--kubeconfig`, else
`$MCLOUD_KUBECONFIG`, else its own, else `$KUBECONFIG`. If Authelia or the
cluster is down, get an admin kubeconfig with `upctl kubernetes config`
(the homelab's break-glass) and pass it with `--kubeconfig`.

GitOps deploys follow the build on GitHub with `$GH_TOKEN` (or
`$GITHUB_TOKEN`), else your GitHub CLI login (`gh auth login`).

## Which app

A command that acts on an app takes its name as the first argument, else
`$MCLOUD_APP`, else the `app:` of the `maxcloud.yaml` in this folder or a
parent. So inside an app's repo, `mcloud status` is enough. `--env staging`
(or `$MCLOUD_ENV`) makes it act on that
[environment or preview](environments-and-previews.md) of the app,
`<app>--staging`, instead: `mcloud logs --env pr-42`.

`apps create`, `set`, `owners` and `delete` change the registry
in the cluster (no commit anywhere), and show the change and ask first.
Pass `--dry-run` to only see it, or `--yes` to skip the question. `delete`
asks you to type the app's name. Apps registered in git
(`platform/apps/maxcloud/apps/`: the platform's own, like run-demo) change
there instead.

## Owners

Whoever registers an app (`mcloud apps create`, or the first `mcloud
deploy`) owns it. An app's owners are admin in its namespace (its pods,
Secrets and database), and only they and admins may change or remove it;
other developers see that it exists, not its data. `mcloud apps owners
<app> --add <user>` and `--remove <user>` change them (Authelia usernames;
an app keeps at least one). Owners never come from `maxcloud.yaml`. An app
without owners is an admin's.

A deleted app's buckets and database backups stay in Object Storage, but
they belong to that registration: registering the name again starts
without them. While the old buckets exist, the new app's Buckets and
Database aren't Ready: `kubectl get maxcloud -n <app>` shows the reason
`RegistrationMismatch`, with the old registration's ID. An admin then
either hands the old data over (the annotation
`maxcloud.gronblom.dev/adopt-registration` on the app's entry) or deletes
the old buckets. How it works:
[the controller's README](../controller/README.md#registrations-who-gets-an-apps-data).

## Output

On a terminal mcloud shows its progress with spinners and colors and asks
with interactive prompts (`mcloud init` asks for the name, language and
deploy mode). In pipes, in CI (`CI`, `GITHUB_ACTIONS`), with `NO_COLOR`,
`TERM=dumb` or `MCLOUD_PLAIN=1`, or with `--plain`, it prints plain lines
instead.

## JSON output

`status`, `apps list`, `history`, `metrics`, `alerts`, `queue ls`, `env
list`, `secrets list`, `db branch list` and `bucket ls` take `--json`: one JSON object
on stdout instead of the table, notes and warnings still on stderr, exit
code as usual. Field names are camelCase and stay: new fields may appear,
existing ones keep their name and meaning. Times are RFC 3339 in UTC; a
list with nothing in it is `[]`; a value that can't be known is `null`.

| Command | Top-level fields |
| --- | --- |
| `apps list` | `apps`: each `name`, `mode` (`gitops`, `direct`), `repo`, `path`, `branch`, `owners` (`[]`: an admin's), `registeredIn` (`cluster`, `git`), `deploy` (`status`, `reason`, `message`, `revision`; GitOps), `sync` (`revision`, `error`: a pushed `maxcloud.yaml` refused) |
| `status` | the same as one app, plus `registered`, `fetch` (Flux reading the repo), `quota`, `services` (`name`, `url`, `public`, `ready`, `revision`, `pods`), `workloads` (`kind`, `name`, `state`, `ready`), `databases` (`name`, `phase`, `instances`, `healthy`), `buckets` (`name`, `bucket`, `public`, `url`, `sizeBytes`, `objects`, `ready`), `domains` (`host`, `service`, `proxied`, `url`, `ready`), `problems` (strings), `alerts` (as `alerts`; `null` when they can't be read) |
| `history` | `app`, `mode`, `repo`, `branch`, `path`, `deploys`: each `id` (for `rollback --to`), `time`, `by`, `image`, `message`, `commit`, `dirty`, `functions`, `rollbackOf`, `current` |
| `metrics` | `app`, `window`, `at`, `services` (`name`, `requests`, `requestsPerSecond`, `errors`, `errorRatio`, `p95Seconds`, `pods`, `podsWanted`), `queues` (`name`, `kind`, `backlog`), `jobs` (`running`, `failed`) |
| `alerts` | `app`, `alerts`: each `name`, `state` (`firing`, `pending`), `severity`, `summary`, `description`, `activeAt`, `value`, `labels` |
| `queue ls` | `app`, `queues` (`name`, `pending`, `running`, `failed`, `done`), `state`, `jobs` (`id`, `queue`, `state`, `attempts`, `created`, `lastError`) |
| `env list` | `app`, `variables`: each `name`, `value`, `from` (`maxcloud.yaml` or `mcloud`) |
| `secrets list` | `app`, `secrets`: each `name` (never values) |
| `db branch list` | `app`, `branches`: each `name`, `phase`, `cluster`, `at`, `fromGeneration`, `created`, `message` |
| `bucket ls` | `app`, `buckets`: each `name`, `bucket`, `public`, `url`, `sizeBytes`, `objects`, `ready`. With a bucket: `app`, `bucket`, `prefix`, `objects`: each `key`, `folder`, `sizeBytes`, `modified` |

A `ready` is a condition: `status` (`True`, `False`, `Unknown`, or `""`
before the controller has looked), `reason`, `message`.

```fish
mcloud status --json | jq -r '.domains[] | select(.ready.status != "True") | "\(.host): \(.ready.message)"'
mcloud apps list --json | jq -r '.apps[] | select(.sync.error) | .name'
```

## Logs, metrics and alerts from the platform

`mcloud logs --since`, `mcloud metrics`, `mcloud alerts` (and the alerts
in `mcloud status`) ask the platform, not the cluster: the maxcloud
controller, on the platform's API gateway
(`/api/v1/apps/<app>/...`). mcloud sends it the token its kubeconfig
uses, and it answers only an app's owners and admins, about that app.
With a kubeconfig that logs in another way (the break-glass admin one),
or on a platform that doesn't offer them yet, mcloud says so; `mcloud
logs` then reads the pods.

## Environment variables

| Variable | |
| --- | --- |
| `MCLOUD_APP` | the app, when there's no argument |
| `MCLOUD_ENV` | the app's environment or preview to act on, as `--env` |
| `MCLOUD_KUBECONFIG` | the kubeconfig, instead of mcloud login's |
| `MCLOUD_PLAIN` | plain output |
| `MCLOUD_PLATFORM_URL` | where the discovery document is; `off` turns discovery off |
| `MCLOUD_VIA` | how `mcloud login` reaches the API server: `auto` (the default), `direct` or `gateway` |
| `MCLOUD_OIDC_ISSUER`, `MCLOUD_OIDC_CLIENT_ID` | the login's OpenID Connect provider and client (`mcloud login --issuer`, `--client-id`) |
| `MCLOUD_IMAGE_REGISTRY` | the maxcloud registry's host (default `registry.gronblom.dev`) |
| `MCLOUD_PACK_FLAGS` | extra flags for `pack build` |
| `MCLOUD_SDK_VERSION` | the `@max-cloud/functions` version `mcloud init` writes, instead of the newest on npm |
| `GH_TOKEN`, `GITHUB_TOKEN` | GitHub access, instead of the GitHub CLI's login |

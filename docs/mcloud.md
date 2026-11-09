# mcloud

mcloud is maxcloud's command line. Every command and flag:
[the reference](reference/cli/README.md).

## Installing

```fish
mise use -g github:maxemiliang/maxcloud   # or: go install github.com/maxemiliang/maxcloud/cmd/mcloud@latest
mcloud login                              # once per machine: log in, and set up cluster access
mcloud doctor                             # if anything's off: says what, and how to fix it
```

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
parent. So inside an app's repo, `mcloud status` is enough.

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

## Environment variables

| Variable | |
| --- | --- |
| `MCLOUD_APP` | the app, when there's no argument |
| `MCLOUD_KUBECONFIG` | the kubeconfig, instead of mcloud login's |
| `MCLOUD_PLAIN` | plain output |
| `MCLOUD_PLATFORM_URL` | where the discovery document is; `off` turns discovery off |
| `MCLOUD_OIDC_ISSUER`, `MCLOUD_OIDC_CLIENT_ID` | the login's OpenID Connect provider and client (`mcloud login --issuer`, `--client-id`) |
| `MCLOUD_IMAGE_REGISTRY` | the maxcloud registry's host (default `registry.gronblom.dev`) |
| `MCLOUD_PACK_FLAGS` | extra flags for `pack build` |
| `MCLOUD_SDK_VERSION` | the `@max-cloud/functions` version `mcloud init` writes, instead of the newest on npm |
| `GH_TOKEN`, `GITHUB_TOKEN` | GitHub access, instead of the GitHub CLI's login |

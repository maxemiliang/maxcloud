# Environment variables

Plain settings (not secrets) reach every function as environment
variables, like a Worker's `vars`. They can live in two places:

- **`env:` in `maxcloud.yaml`**, kept in git with the code. The deploy
  writes them into the functions' manifests, so a change ships with the
  next deploy (a new revision) and can be reverted like any other.
  Values are taken exactly as written: `RATIO: 0.10` is `0.10`, not
  `0.1`.

  ```yaml
  env:
    LOG_LEVEL: info
    API_BASE: https://api.example.com
  ```

- **`mcloud env set`**, for values that change without a deploy. They're
  kept in the ConfigMap `app-env` in the app's namespace, and the app's
  HTTP and queue functions restart so they have the new value right away
  (`--no-restart`: on their next start). The maxcloud controller keeps a
  copy and puts it back after a cluster rebuild.

  ```fish
  mcloud env set LOG_LEVEL=debug FEATURE_X=on
  mcloud env get LOG_LEVEL
  mcloud env list                     # NAME=VALUE lines, both kinds
  mcloud env unset FEATURE_X
  ```

`maxcloud.yaml` wins: `mcloud env set` refuses a name it declares. A name
is either a variable or a [secret](secrets.md), not both, and the
platform's own (`DATABASE_URL`, `PORT`, `AWS_*`, `MAXCLOUD_*`, `K_*`) are
off limits. `mcloud dev` gives the app `maxcloud.yaml`'s env (`.env`
overrides it locally), plus the cluster's with `--secrets remote`. Only
functions get `env:` from `maxcloud.yaml`; an app with its own manifests
can use `app-env` with `envFrom: [{configMapRef: {name: app-env, optional:
true}}]`.

The commands: [`mcloud env`](reference/cli/mcloud-env.md).

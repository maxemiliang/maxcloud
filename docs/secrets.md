# Secrets and key-value storage

`mcloud secrets set NAME` stores an app's secret, like `wrangler secret
put`: nothing to commit and no keys to hold, and the app's HTTP and queue
functions restart so they have it right away. Every function gets the
secrets as environment variables, from the Secret `app-secrets` in the
app's namespace; the SDKs' `secret("NAME")` reads one. Settings that
aren't secret are [environment variables](environment.md) instead.

```fish
mcloud secrets set STRIPE_KEY       # asks for the value; restarts the functions
mcloud secrets list                 # names only
mcloud secrets unset STRIPE_KEY
```

Where they're kept depends on the platform:

- **On a platform whose apps API offers secrets** (its discovery document
  lists `secrets`), they're kept in OpenBao, and External Secrets Operator
  copies them into the Secret. mcloud sets and removes them through the
  API, as you: the app's owners and admins may. It waits until the Secret
  has the change (up to 30 seconds) before it restarts the functions. If
  that takes longer, mcloud says so and doesn't restart: the functions get
  the change when they next start after the sync. `mcloud secrets list
  --json` also shows the secrets' version.
- **On older platforms** mcloud edits the Secret itself, and the maxcloud
  controller keeps an encrypted copy and puts it back after a cluster
  rebuild.

A secret's name is an environment variable's (`A-Z`, `0-9` and `_`, not
starting with a digit), and not one the platform sets itself:
`DATABASE_URL`, `FUNCTION_TARGET`, `PORT`, or anything starting with
`AWS_`, `MAXCLOUD_` or `K_`. It can't be the name of one of the app's
environment variables either.

`mcloud dev --secrets remote` reads the Secret, on any platform.

For KV, the SDKs' `kv` stores JSON values in the app's database.

## Secrets in git

GitOps apps used to keep their secrets SOPS-encrypted in
`deploy/k8s/secrets.sops.yaml`. The homelab's Flux stopped decrypting
apps' SOPS files on 2026-11-07, so an app that still has the file can't
deploy until it's gone. `mcloud secrets migrate` moves the app off it: it
takes over the Secret `app-secrets` that Flux decrypted from the file
earlier, and removes the file from the repo. Commit and push that.

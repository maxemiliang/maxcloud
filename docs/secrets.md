# Secrets and key-value storage

`mcloud secrets set NAME` stores an app's secret, like `wrangler secret
put`. Every app keeps them in the Secret `app-secrets` in its namespace:
nothing to commit and no keys to hold, and the app's HTTP and queue
functions restart so they have it right away. Every function gets the
secrets as environment variables; the SDKs' `secret("NAME")` reads one. The
maxcloud controller keeps an encrypted copy and puts it back after a
cluster rebuild. Settings that aren't secret are
[environment variables](environment.md) instead.

```fish
mcloud secrets set STRIPE_KEY       # asks for the value; restarts the functions
mcloud secrets list                 # names only
mcloud secrets unset STRIPE_KEY
```

For KV, the SDKs' `kv` stores JSON values in the app's database.

## Secrets in git

GitOps apps used to keep their secrets SOPS-encrypted in
`deploy/k8s/secrets.sops.yaml`. The homelab's Flux stopped decrypting
apps' SOPS files on 2026-11-07, so an app that still has the file can't
deploy until it's gone. `mcloud secrets migrate` moves the app off it: it
takes over the Secret `app-secrets` that Flux decrypted from the file
earlier, and removes the file from the repo. Commit and push that.

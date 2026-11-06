# Secrets and key-value storage

`mcloud secrets set NAME` stores an app's secret, like `wrangler secret
put`. Every app keeps them in the Secret `app-secrets` in its namespace:
nothing to commit and no keys to hold, and the app's HTTP and queue
functions restart so they have it right away. Every function gets the
secrets as environment variables; the SDKs' `secret("NAME")` reads one. The
maxcloud controller keeps an encrypted copy and puts it back after a
cluster rebuild. A GitOps app that still has
`deploy/k8s/secrets.sops.yaml` (how secrets were kept before) moves off it
with `mcloud secrets migrate`, which takes over the Secret Flux already
decrypted.

```fish
mcloud secrets set STRIPE_KEY       # asks for the value; restarts the functions
mcloud secrets list                 # names only
mcloud secrets unset STRIPE_KEY
```

For KV, the SDKs' `kv` stores JSON values in the app's database.

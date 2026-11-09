# Custom domains

A function (or any Knative service of the app) can have hostnames besides
`<service>--<app>.gronblom.dev`, from `domains:` in `maxcloud.yaml`:

```yaml
domains:
  - {host: paja.lankkupelit.fi, service: web, proxied: true}
```

- **An admin allows them first:**
  `platform/infrastructure/configs/run/domains.yaml` lists the hostnames each app
  may have. Admission refuses a registry entry (from `mcloud deploy`, or a
  pushed `maxcloud.yaml`) with any other, and refuses any route in the
  app's namespace with a hostname that isn't the app's.
- **Zones the cluster has certificates for:** one level below
  `gronblom.dev`, `lab.gronblom.dev` or `lankkupelit.fi`. external-dns
  makes the DNS record. `proxied: true` puts it behind Cloudflare (WAF,
  caching, Cloudflare Access), not for `*.lab.gronblom.dev`.
- **`lab.gronblom.dev` is for apps trusted like an admin.** Authelia's
  session cookie covers all of `lab.gronblom.dev`, so an app there gets
  the session of every admin who opens it, which is enough to log in to
  the cluster as them. No app gets `<app>.lab.gronblom.dev` by default; an
  admin adds one to the list only for an app they trust that far.
- **The service can stay private.** The hostname reaches its revisions
  directly, scale-to-zero included, and the app sees the `Host` the
  client sent (`paja.lankkupelit.fi`, not a cluster-local name).
- `kubectl get maxcloud -n <app>` shows each Domain. One that isn't Ready
  says why: not on the admin's list, no certificate covers it, the
  service isn't deployed yet, or another route already has the hostname
  (it takes over once that one is gone).
- Removing a domain from the list removes its route and DNS record.

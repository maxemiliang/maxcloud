---
title: Traefik 3 gives a request 60s, body included; the LB is not the limit
kind: gotcha
area: networking
tags: [traefik, timeout, upload, registry, load-balancer]
learned: 2026-11-06
updated: 2026-11-06
where:
  - infrastructure/controllers/traefik/release.yaml
---

# Traefik 3 gives a request 60s, body included; the LB is not the limit

**Symptom:** a long upload through Traefik (an image layer pushed to
`registry.gronblom.dev`) is cut off after a minute.

**Cause:** Traefik 3's entrypoint `transport.respondingTimeouts.readTimeout`
defaults to 60s, and covers reading the whole request, body included
(Traefik docs, EntryPoints). writeTimeout defaults to 0, idleTimeout to
180s.

**Fix:** `ports.websecure.transport.respondingTimeouts.readTimeout: 15m` in
the Traefik chart values. It applies to every HTTPS route; a slow client
can hold a connection that long.

**The UpCloud load balancer isn't a limit here:** its `timeout_client` and
`timeout_server` are 10s, but in TCP mode HAProxy switches to
`timeout_tunnel` (3600s of inactivity) once the connection is established.
Read them with `upctl loadbalancer show <uuid> -o json`.

See also: [lab-subdomain-dns-only](lab-subdomain-dns-only.md) (why the
registry is DNS-only: Cloudflare's 100 MB body cap).

---
title: maxcloud custom domains cover the managed zones only; other people's domains are a later step
kind: decision
area: networking
tags: [maxcloud, custom-domains, dns, cname, tls, cert-manager, cloudflare, gateway-api, traefik, external-dns]
learned: 2026-11-06
updated: 2026-11-13
where:
  - infrastructure/configs/gateway/gateway.yaml
  - infrastructure/configs/gateway/certificate.yaml
  - infrastructure/controllers/external-dns/release.yaml
---

# maxcloud custom domains cover the managed zones only; other people's domains are a later step

**Decision:** not now (2026-11-06). `domains:` in `maxcloud.yaml` takes
hostnames in the zones the cluster already manages (makiya.dev,
gronblom.dev, lankkupelit.fi), and only the ones an admin has allowed for the app.
external-dns makes the record; the Gateway's wildcard listeners already
have the certificates. A domain in a zone we don't manage (`example.com`
pointing at function `test` of app `example`) isn't supported yet.

**Why a CNAME alone isn't enough:** with `example.com CNAME
test--example.makiya.dev`, DNS reaches the load balancer, but:

- **TLS fails.** The HTTPS listeners hold only `*.makiya.dev`,
  `*.gronblom.dev`, `*.lab.gronblom.dev` and `*.lankkupelit.fi`, and each
  listener only
  takes routes for its own hostname. The host needs its own certificate.
  cert-manager can get one with HTTP-01 once the CNAME is there; DNS-01
  isn't possible without a token for their zone. It also needs a listener
  that accepts the host: one per host, or a listener without a hostname
  whose certificateRefs the platform keeps up to date. Check that Traefik
  picks the certificate by SNI from several refs before relying on that.
- **Routing fails.** Traefik routes by Host, and nothing routes
  `example.com` (404). The domain route maxcloud generates covers this, the
  same route as for managed zones.
- **Apex names:** `example.com` itself can't be a CNAME at most DNS
  providers (Cloudflare flattens it; elsewhere ALIAS/ANAME or an A record
  to the LB's address, which can change).
- **Cloudflare-proxied targets:** a CNAME from another Cloudflare account
  to a proxied makiya.dev name fails with error 1014 ("CNAME Cross-User
  Banned"). From another DNS provider, Cloudflare gets a hostname it has no
  zone for and refuses it. It only works if `example.com` is a zone in the
  same Cloudflare account. The general way to proxy it is Cloudflare for
  SaaS custom hostnames: Cloudflare issues the edge certificate. That
  needs SSL and Certificates permission on the makiya.dev zone, which
  the external-dns token doesn't have.
- **Squatting:** if anyone can register a domain they don't control, an
  app can take `example.com` before its owner points DNS at us.

**The plan, when it's wanted:**

- `domains: [{host: example.com, service: test, dns: external}]`. The
  platform makes no DNS record. It prints the CNAME target: the load
  balancer's hostname, not `test--example.makiya.dev`, so it also works
  for a private function and doesn't depend on that name being DNS-only.
- Ownership: a TXT record such as `_maxcloud.example.com TXT app=example`,
  checked by the controller before it routes the host. The admin allowlist
  stays as a second gate.
- TLS: a cert-manager Certificate per host (HTTP-01 through the `web`
  listener), plus the listener handling above. Or Cloudflare for SaaS, if
  the host should be proxied.
- The route itself doesn't change: the controller's domain HTTPRoute,
  with the original Host header.

**Revisit when:** an app needs a domain outside makiya.dev, gronblom.dev
and lankkupelit.fi, or a zone of ours moves out of the Cloudflare account.

See also: [custom-domains-copy-route](../knative/custom-domains-copy-route.md),
[lab-subdomain-dns-only](lab-subdomain-dns-only.md),
[makiya-dev-app-domain](makiya-dev-app-domain.md),
[traefik-externalname-backends](traefik-externalname-backends.md).

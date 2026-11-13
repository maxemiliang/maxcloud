---
title: *.lab.gronblom.dev is DNS-only; one-level names may be proxied
kind: decision
area: networking
tags: [cloudflare, dns, tls, cert-manager]
learned: 2026-10-29
updated: 2026-11-13
where:
  - infrastructure/configs/gateway/gateway.yaml
---

# *.lab.gronblom.dev is DNS-only; one-level names may be proxied

**Decision:** two-level names (`*.lab.gronblom.dev`, SSO) are DNS-only;
one-level names (`*.makiya.dev`, `*.gronblom.dev`) may go through
Cloudflare's proxy, opt-in
per service (`external-dns.kubernetes.io/cloudflare-proxied`). Two wildcard
certificates from Let's Encrypt (DNS-01) on two HTTPS listeners.

**Why:** Cloudflare's free Universal SSL certificate covers only one level
of subdomain, so a proxied `x.lab.gronblom.dev` would fail TLS at the edge.

**Rejected:** Cloudflare's Advanced Certificate Manager (paid) for
two-level names.

**Also:** a name created by external-dns can stay unresolvable on a machine
that looked it up before it existed (negative caching); public resolvers
(1.1.1.1) have it within a minute.

See also: [custom-domains-outside-managed-zones](custom-domains-outside-managed-zones.md),
[makiya-dev-app-domain](makiya-dev-app-domain.md),
and `private/docs/kb/networking/lab-hosts-see-admin-sessions.md`.

---
title: maxcloud apps live on makiya.dev; gronblom.dev keeps the platform's own hosts
kind: decision
area: networking
tags: [maxcloud, dns, cloudflare, knative, cert-manager, external-dns, gateway-api, cookies]
learned: 2026-11-13
updated: 2026-11-13
where:
  - infrastructure/configs/knative/serving.yaml
  - infrastructure/configs/gateway/gateway.yaml
  - infrastructure/configs/gateway/certificate.yaml
  - infrastructure/controllers/maxcloud/config.yaml
  - infrastructure/configs/run/traefik-ingress.yaml
---

# maxcloud apps live on makiya.dev; gronblom.dev keeps the platform's own hosts

**Decision (2026-11-13):** apps' generated names are
`<service>--<app>.makiya.dev` (Knative's domain) and public buckets'
`<bucket>--<app>.makiya.dev` (`MAXCLOUD_PUBLIC_DOMAIN`), on the Gateway
listener `https-makiya` with its own wildcard certificate. gronblom.dev
keeps the platform's hosts (`registry`, `maxcloud` discovery, `hello`,
`*.lab` admin tools and SSO) and custom domains an admin allows an app.

**Why:**

- Apps are a different site from the platform. Before, an app at
  `x--app.gronblom.dev` was same-site with `registry.gronblom.dev` and
  `auth.lab.gronblom.dev`, so `SameSite=Lax` cookies didn't separate them
  (`private/docs/kb/networking/lab-hosts-see-admin-sessions.md`).
- A zone that is all apps can go on the Public Suffix List later, so one
  app can't set cookies for every other (like `herokuapp.com`).

**What a move like this touches** (all must change together):

- Cloudflare: both API tokens (cert-manager's and external-dns's) need DNS
  edit on the new zone, and the zone needs SSL mode Full (strict) for
  proxied names. Neither is in this repo.
- cert-manager's `dnsZones`, external-dns's `domainFilters`, a wildcard
  Certificate and a Gateway listener.
- Knative's `domain` key, and the controller's `MAXCLOUD_PUBLIC_DOMAIN`
  and `MAXCLOUD_PUBLIC_GATEWAY` (read at start: restart the controller).
- Cilium's 8081 host rule in `configs/run/traefik-ingress.yaml`: without
  it apps can't reach each other's public names in the cluster.
- Old names stop working: Knative serves a service on one domain, and
  external-dns deletes the old records. Only demos used them then.

**Rejected:** keeping apps on gronblom.dev under a second level
(`*.apps.gronblom.dev`): still the same site as the platform, and
Cloudflare's free edge certificate doesn't cover two-level names.

**Revisit when:** makiya.dev goes on the Public Suffix List (update this),
or the platform's own hosts should move too (registry and discovery are
in `mcloud`'s defaults and every app's pull secret).

See also: [lab-subdomain-dns-only](lab-subdomain-dns-only.md),
[custom-domains-outside-managed-zones](custom-domains-outside-managed-zones.md).

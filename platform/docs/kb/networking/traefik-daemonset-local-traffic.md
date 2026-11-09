---
title: Traefik runs as a DaemonSet with externalTrafficPolicy Local
kind: decision
area: networking
tags: [traefik, upcloud, load-balancer, proxy-protocol]
learned: 2026-10-29
updated: 2026-10-29
where:
  - infrastructure/controllers/traefik/release.yaml
---

# Traefik runs as a DaemonSet with externalTrafficPolicy Local

**Decision:** one Traefik pod per node (DaemonSet), Service
`externalTrafficPolicy: Local`, UpCloud load balancer in TCP mode with PROXY
protocol v2.

**Why:** with the default Cluster policy, traffic arriving at a node without
Traefik was SNATed to the `cilium_host` address on its way to the other
node, so Traefik no longer trusted the PROXY header: about half of all
connections failed. With Local, a node only gets traffic if it runs Traefik
itself, and the client address survives.

**Also:** the UpCloud CCM's default plan and mode (development, HTTP) were
wrong for this; the Service annotations set them. The LB's health checks
were tightened to every 2 s, 2 failures out, 2 successes back in: a drain
now drops about 5 s of traffic instead of about 45 s.

**Revisit when:** zero-drop drains matter (kured pre-reboot labels, graceful
Traefik shutdown, rebootDelay), or UpCloud's LB gains connection draining.

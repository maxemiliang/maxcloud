---
title: Agent sessions run under gVisor through agent-sandbox, and reach only the Claude API
kind: decision
area: ops
tags: [sandbox, gvisor, agent-sandbox, claude-code, cilium, egress, flux]
learned: 2026-11-10
updated: 2026-11-10
where:
  - clusters/personal/sandbox.yaml
  - sandbox/controllers/agent-sandbox/release.yaml
  - sandbox/sessions/claude-code.yaml
  - sandbox/sessions/network.yaml
  - sandbox/image/Dockerfile
  - docs/runbooks/sandbox-sessions.md
---

# Agent sessions run under gVisor through agent-sandbox, and reach only the Claude API

**Decision:** a Claude Code session is one pod in the namespace `sandbox`,
on the tainted `sandbox` node group, under gVisor. agent-sandbox
(kubernetes-sigs) makes the pod from a `SandboxClaim`. The pod has Claude
Code in its image and the Claude subscription token in its environment, and
a Cilium policy lets it reach `api.anthropic.com` and nothing else. All of
it is two Flux Kustomizations of their own (`sandbox-controllers`,
`sandbox-sessions`).

**Why:**

- **gVisor:** a session runs whatever a prompt or a file talks it into, so
  it gets its own kernel boundary. What gVisor costs and how it's installed:
  [sandbox-gvisor-on-uks](sandbox-gvisor-on-uks.md).
- **The subscription token, not an API key:** sessions are the owner's own,
  and `claude setup-token` gives a one-year token for model requests only.
  Anthropic doesn't allow offering a subscription's login to other people
  through a product, so this is not for apps' users.
- **Only the Claude API:** the token is in the pod, where the session can
  read it. With one allowed host there is nowhere else to send it. So the
  image has Claude Code in it (no package registry at run time), and work
  goes in and out with `kubectl cp`.
- **agent-sandbox:** a claim gives a pod with a stable name, an expiry,
  and suspend and resume, without writing a controller.
- **Its own Kustomizations:** the installer DaemonSet is Ready only when
  every sandbox node has gVisor. In `infra-controllers`, a node that can't
  install it would hold back `infra-configs` and `apps`.

**Rejected:**

- **Kata Containers:** can't run, no nested virtualisation on these nodes.
- **A proxy that adds the credential outside the pod:** the right design
  for an API key. Not documented for subscription tokens.
- **A warm pool with idle pods:** the pool has `replicas: 0`. An idle pod
  would hold the token and a quarter of a core, to save a few seconds.
- **agent-sandbox's own NetworkPolicy** (`networkPolicyManagement`
  `Managed`, the default): it allows the whole public internet, and
  policies add up, so it would undo the Cilium one.
- **The npm registry on the allowlist:** it accepts uploads, and shares its
  addresses with every other site behind Cloudflare (the policy allows
  addresses, not names).
- **The maxcloud registry for the image:** its access rules are per app,
  made by the maxcloud controller from the app registry. The image is on
  GHCR, like the controller's.
- **A volume for `/work`:** an emptyDir until a session's work needs to
  outlive its pod. Suspending a session loses its work for now.
- **The cluster autoscaler:** one node holds four sessions, and UpCloud's
  provider can't scale from zero
  ([node-autoscaling-burst-pool](node-autoscaling-burst-pool.md)).

**Not a boundary:** the taint. An app that adds the toleration, or
`runtimeClassName: gvisor`, can run pods on the sandbox node within its own
quota. Sessions rank above app pods, so they push those off when the node
is full.

**Revisit when:** sessions must reach something in the cluster (ClusterIPs
don't work under gVisor here:
[gvisor-no-clusterip](../networking/gvisor-no-clusterip.md)), someone other
than an admin should start them, four at a time isn't enough, or Anthropic
documents a way to keep the subscription token out of the pod.

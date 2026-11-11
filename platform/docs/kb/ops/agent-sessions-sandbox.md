---
title: Agent sessions run under gVisor through agent-sandbox, one sandbox per user, and reach the internet but not the cluster
kind: decision
area: ops
tags: [sandbox, gvisor, agent-sandbox, claude-code, cilium, egress, flux, mcloud, admission]
learned: 2026-11-10
updated: 2026-11-11
where:
  - clusters/personal/sandbox.yaml
  - sandbox/controllers/agent-sandbox/release.yaml
  - sandbox/sessions/users.yaml
  - sandbox/sessions/rbac.yaml
  - sandbox/sessions/admission.yaml
  - sandbox/image/Dockerfile
  - docs/runbooks/sandbox-sessions.md
---

# Agent sessions run under gVisor through agent-sandbox, one sandbox per user, and reach the internet but not the cluster

**Decision:** a Claude Code session is one pod on the tainted `sandbox`
node group, under gVisor, made by agent-sandbox (kubernetes-sigs) from a
`SandboxClaim`. Every user has a sandbox namespace of their own
(`sbx-<user>`), which a ResourceSet makes from an entry that `mcloud
sandbox` creates the first time they use it. The namespace holds their
Claude subscription token, which they set themselves, and their sessions.
The pod has Claude Code in its image and the token in its environment, and
a Cilium policy lets it reach the public internet and nothing in the
cluster. All of it is two Flux Kustomizations of their own (`sandbox-controllers`,
`sandbox-sessions`).

**Why:**

- **gVisor:** a session runs whatever a prompt or a file talks it into, so
  it gets its own kernel boundary. What gVisor costs and how it's installed:
  [sandbox-gvisor-on-uks](sandbox-gvisor-on-uks.md).
- **Each user's own subscription token:** `claude setup-token` gives a
  one-year token for model requests only. Anthropic doesn't allow offering
  one subscription's login to other people, so every user brings theirs.
- **A namespace per user:** a shell in a session can read the token in it,
  and RBAC limits shells (`pods/exec`) per namespace, not per pod. In one
  shared namespace every user could read every token.
- **Self-service, like apps:** an entry in `flux-system`, which
  `maxcloud-developers` may already create, turned into a namespace by a
  ResourceSet. Admission ties an entry to the Authelia user who made it
  (`maxcloud-sandbox-entries`), so nobody enrols, changes or removes
  someone else.
- **The token set through mcloud, not kept in git:** it's the user's, not
  the platform's. It goes straight into their namespace, where a second
  admission policy allows that one Secret and no other.
- **The internet, not the cluster** (decided 2026-11-11): a session that
  can't clone, install a package or look anything up is of little use for
  real work. The policy allows every public address on any port, and
  excepts the private ranges, 100.64.0.0/10 (UKS's API server and load
  balancers) and link-local (the metadata service); a deny on the
  `cluster` and `kube-apiserver` entities says the same by identity.
  **What that costs:** the token is in the pod, where the session can read
  it, and a session runs whatever a prompt, a web page or a repository
  talks it into. It can now send the token, and the code in the session,
  anywhere. What still holds is everything else: the session's own kernel
  boundary, no credentials but that token, nothing reachable in the
  cluster.
- **agent-sandbox:** a claim gives a pod with a stable name and an expiry,
  without writing a controller. Users get claims, not pods: a session is
  always the template's pod.
- **Its own Kustomizations:** the installer DaemonSet is Ready only when
  every sandbox node has gVisor. In `infra-controllers`, a node that can't
  install it would hold back `infra-configs` and `apps`.

**Rejected:**

- **Kata Containers:** can't run, no nested virtualisation on these nodes.
- **One token for everyone, SOPS-encrypted in the private repo:** the first
  design. One person's subscription for every user, and only an admin
  could change it.
- **A list of users in git:** enrolment would be an admin's commit, when
  the registry and its admission pattern were already there.
- **A proxy that adds the credential outside the pod:** the right design
  for an API key. Not documented for subscription tokens.
- **A warm pool with idle pods:** the pool has `replicas: 0`. An idle pod
  would hold the token and a quarter of a core, to save a few seconds.
- **Only the Claude API** (the first policy, until 2026-11-11): DNS for,
  and port 443 of, `api.anthropic.com` alone, so there was nowhere to send
  the token but the host it's for. Everything had to come in with `mcloud
  sandbox cp`, dependencies included. An allowlist of a few more hosts
  gives up the same thing: a package registry accepts uploads, and Cilium
  allows addresses, not names, which a registry behind Cloudflare shares
  with every other site there.
- **agent-sandbox's own NetworkPolicy** (`networkPolicyManagement`
  `Managed`, the default): it allows the public internet too, but
  100.64.0.0/10 isn't among its exceptions, and policies add up.
- **The maxcloud registry for the image:** its access rules are per app,
  made by the maxcloud controller from the app registry. The image is on
  GHCR, like the controller's.
- **A volume for `/work`:** an emptyDir until a session's work needs to
  outlive its pod.
- **Backing up entries and tokens** (as the maxcloud controller does for
  apps): after a rebuild a user sets their token again, which is one
  command.
- **The cluster autoscaler:** one node holds four sessions, and UpCloud's
  provider can't scale from zero
  ([node-autoscaling-burst-pool](node-autoscaling-burst-pool.md)).

**Shared with everything else:** the NAT gateway. What a session does on
the internet comes from the cluster's address, the same one every app
leaves from.

**Not a boundary:** the taint. An app that adds the toleration, or
`runtimeClassName: gvisor`, can run pods on the sandbox node within its own
quota. Sessions rank above app pods, so they push those off when the node
is full. Nor is a sandbox entry's name: any developer can create a
provider called `sandbox-<someone>` without the label, which keeps that
user from enrolling until an admin deletes it.

**Revisit when:** a token or code leaves a session that shouldn't have
(back to an allowlist, or a proxy), sessions must reach something in the
cluster (ClusterIPs
don't work under gVisor here:
[gvisor-no-clusterip](../networking/gvisor-no-clusterip.md)), four at a
time isn't enough, usernames that can't be namespace names need sandboxes,
or Anthropic documents a way to keep the subscription token out of the pod.

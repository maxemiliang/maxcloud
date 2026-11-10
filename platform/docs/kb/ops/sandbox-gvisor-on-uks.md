---
title: gVisor installs and runs on a UKS node; Kata can't (no nested virtualisation)
kind: fact
area: ops
tags: [gvisor, kata, sandbox, containerd, upcloud, node-group, claude-code, cilium, egress]
learned: 2026-11-10
updated: 2026-11-10
where:
  - infra/cluster/nodes.tf
  - sandbox/controllers/runtime/install.sh
  - sandbox/controllers/runtime/installer.yaml
  - sandbox/sessions/users.yaml
---

# gVisor installs and runs on a UKS node; Kata can't (no nested virtualisation)

Measured 2026-11-10 on the `sandbox` node group (one STARTER-2xCPU-4GB
node, UKS 1.36.4), in a spike applied by hand: the installer that is now
`sandbox/controllers/runtime`, and two test pods on the node, the same
except for the runtime. What was built on these answers:
[agent-sessions-sandbox](agent-sessions-sandbox.md).

- **The node:** Debian 13, kernel 6.12.113, containerd 2.2.4 with
  `/etc/containerd/config.toml` at `version = 3` and no imports, cgroups v2.
- **gVisor works.** A privileged DaemonSet wrote `runsc` (20261102) to
  `/usr/local/bin`, appended the runtime to containerd's config and
  restarted containerd; the node stayed Ready. A Pod Security `restricted`
  pod with `runtimeClassName: gvisor` runs (`/proc/version`:
  `4.19.0-gvisor`). UKS has no hook for node setup, so this DaemonSet is
  the only way, and UpCloud doesn't support it.
- **Kata Containers is out.** No `vmx`/`svm` CPU flag and no `/dev/kvm` on
  the node: this plan doesn't pass nested virtualisation through.
- **Speed, the same work under runc and gVisor on that node:**

  | | runc | gVisor |
  |---|---|---|
  | `npm install -g @anthropic-ai/claude-code` | 4.0 s | 5.7 s |
  | `npm install typescript eslint vite` (1,973 files) | 8.4 s | 11.1 s |
  | read every one of those files | 0.10 s | 0.39 s |

  So about 1.3 to 1.4 times slower for installs and close to 4 times for
  reading many small files.
- **A Claude Code session works under it, on a subscription.** `claude -p`
  (2.1.322) in the gVisor pod, with `CLAUDE_CODE_OAUTH_TOKEN` set to a token
  from `claude setup-token`, ran through. No API key involved.
- **ClusterIP services don't work from a gVisor pod**, cluster DNS
  included: [gvisor-no-clusterip](../networking/gvisor-no-clusterip.md).
- **Cilium's rules by DNS name work for a gVisor pod.** With a
  CiliumNetworkPolicy on the namespace that allowed only DNS for, and port
  443 of, `api.anthropic.com` and `registry.npmjs.org` (what the policy in
  `sandbox/sessions/users.yaml` grew from), both the runc and the gVisor
  pod resolved and reached those two hosts, got no answer for `example.com` (`EAI_AGAIN`), and timed
  out on the resolvers' port 443 and on the API Service's ClusterIP. So
  Cilium 1.18.6's DNS proxy sees gVisor's queries, as long as they go to a
  resolver by its own address and not a ClusterIP. A whole `claude -p`
  session (write a file, run it) then ran in the gVisor pod with the policy
  on: Claude Code doesn't mind the hosts it can't reach (telemetry, update
  checks). The npm registry was still allowed in that run. With only
  `api.anthropic.com` resolvable (the session image in Docker, 2026-11-10)
  `claude -p` reaches the API within seconds.
- **A tainted node without Traefik costs no traffic.** With the sandbox
  node in the cluster, 30 of 30 requests to hello.gronblom.dev and 15 of 15
  to grafana.lab.gronblom.dev (DNS-only, straight to the load balancer)
  were answered. Its member status in the Hub wasn't looked at.
- **On the node:** Cilium, the CSI node plugin, konnectivity and
  node-exporter. Traefik, Alloy and kured don't tolerate the taint and
  stay off, so the node's pod logs don't reach Loki yet.

- **The install survives a node restart.** After restarting the server
  from the Hub, the installer started again and logged only its `fact`
  lines: no `installed`, no `registered`, so `runsc` and the config entry
  were both still there, and a gVisor pod started. In the 2 h 40 min before
  the restart nothing rewrote the config either (one `registered runsc`
  line in that log).

**Not known yet:** whether anything rewrites the config over days or on a
UKS node upgrade. The installer puts the entry back within five minutes and
logs a second `registered runsc` line when it does.
A replaced node comes up without `runsc` until the installer has run on it.

See also: [agent-sessions-sandbox](agent-sessions-sandbox.md),
[node-autoscaling-burst-pool](node-autoscaling-burst-pool.md),
[gvisor-no-clusterip](../networking/gvisor-no-clusterip.md).

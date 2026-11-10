---
title: ClusterIP services time out from gVisor pods; pod IPs and the internet work
kind: gotcha
area: networking
tags: [gvisor, cilium, clusterip, dns, sandbox, kube-proxy-replacement]
learned: 2026-11-10
updated: 2026-11-10
where:
  - docs/runbooks/sandbox-spike/test.yaml
  - docs/runbooks/sandbox-spike.md
---

# ClusterIP services time out from gVisor pods; pod IPs and the internet work

**Symptom:** in a pod with `runtimeClassName: gvisor`, anything that
resolves a name hangs: `npm install` sits there using no CPU
(`kubectl top pod`: 1m). The same pod under runc on the same node is fine.

**Cause:** from a gVisor pod, connections to any ClusterIP time out,
cluster DNS (10.96.0.10) included. Measured 2026-11-10 on the sandbox node
(UKS 1.36.4, Cilium 1.18.6 with `kube-proxy-replacement: "true"`, gVisor
20261102):

| From the pod | runc | gVisor |
|---|---|---|
| TCP to 1.1.1.1:443 | connects | connects |
| DNS asked of 1.1.1.1 | answers | answers |
| DNS asked of a CoreDNS pod IP | answers | answers |
| TCP to 10.96.0.10:53 (DNS Service) | connects | times out |
| TCP to 10.96.0.1:443 (API Service) | connects | times out |

Cilium translates a Service's address when the pod's process calls
`connect()` (its socket load balancer, part of replacing kube-proxy), and
gVisor's own network stack never makes that call on the node's kernel. The
same happens on a local kind cluster with Cilium 1.20.4 and
`kubeProxyReplacement=true`, so it's Cilium and gVisor, not UKS.

**Fix:** for the spike, the test pods use public resolvers
(`dnsPolicy: None`, `docs/runbooks/sandbox-spike/test.yaml`). They can't
resolve cluster names, which a sandbox for untrusted code shouldn't need.

What a sandbox must reach inside the cluster (an egress proxy, say) can't
sit behind a ClusterIP as things are. Either it's reached by pod IP, or
Cilium keeps its socket load balancer to the host namespace: the Helm
value `socketLB.hostNamespaceOnly=true` (`bpf-lb-sock-hostns-only: "true"`
in `cilium-config`). On that kind cluster, setting it and restarting the
agents made every ClusterIP work from the gVisor pod. Cilium here is
UpCloud's Helm release, so on UKS it's a question for UpCloud, and not
tried.

**Recognise it:** in the pod,
`node -e 'require("dns").promises.lookup("example.com").then(console.log)'`
hangs, while the same with a resolver set to 1.1.1.1 answers.

See also: [sandbox-gvisor-on-uks](../ops/sandbox-gvisor-on-uks.md),
[the spike's runbook](../../runbooks/sandbox-spike.md).

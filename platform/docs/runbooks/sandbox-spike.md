# Sandbox runtime spike: a node for agent sessions, and gVisor on it

One tainted node (`sandbox` node group) and gVisor installed on it by hand,
to answer what we can't know without a real UKS node, before building
anything on top (cluster autoscaler, agent-sandbox, the credentials proxy):

1. Can gVisor be installed on a UKS node at all? UKS has no hook for node
   setup, so a privileged DaemonSet writes `runsc` to the node and adds it to
   containerd's config. UpCloud doesn't support this.
2. Does it stay installed (a reboot, UKS rewriting the config)?
3. Is Claude Code usable under it? gVisor makes file-heavy work slower.
4. Is Kata Containers an option instead (`/dev/kvm` on the node)?
5. What does a tainted node without Traefik do to the load balancer?
6. Can a session be held to the few hosts it needs (Cilium's rules by DNS
   name, for a gVisor pod)?

**All of it was run on the cluster on 2026-11-10**, and the answers are in
[sandbox-gvisor-on-uks](../kb/ops/sandbox-gvisor-on-uks.md): gVisor
installs, survives a restart, runs Claude Code on a subscription, and can
be held to two hosts; Kata can't run. One thing is left of step 5: the
installer hasn't run for a whole day yet. Fix this page where reality
differs.

**Tried on a local kind cluster** (2026-11-10; kind 0.33.0, Kubernetes
1.36.4, containerd 2.3.4, gVisor 20261102), which says the manifests work,
not that UKS lets them:

- The installer installed and registered `runsc` with containerd config
  version 2 and version 4, ran a second time without changing anything, and
  installed nothing when the checksum didn't match.
- A `runtimeClassName: gvisor` pod under Pod Security `restricted` started,
  with `/proc/version` saying `4.19.0-gvisor`. A pod created before `runsc`
  was registered started by itself once it was.
- Claude Code 2.1.322 installed and started in it. `npm install typescript
  eslint vite` took 12 s against 7 s under runc, and reading the 1,973 files
  it wrote 329 ms against 96 ms.

- The egress policy of step 4, on kind with Cilium 1.20.4 (1.18.6, which
  UKS runs, doesn't start on the test machine's 7.2 kernel): Cilium accepted
  it and it dropped everything it should (the resolvers' port 443, a
  ClusterIP, under runc and gVisor). **Its allow side could not be tested:**
  on that cluster Cilium's DNS proxy never received a query, so allowed
  names timed out too. That was the kind cluster, not the policy: on UKS
  the allowed names work (step 4).

Cost while it runs: one STARTER-2xCPU-4GB node, about €12/month (billed by
the hour; the price is the estimate in
[node-autoscaling-burst-pool](../kb/ops/node-autoscaling-burst-pool.md),
check it in the Hub).

Commands are fish, run from `platform/`.

## 1. Add the node group

`infra/cluster/nodes.tf` has the group (`upcloud_kubernetes_node_group.sandbox`).
The plan must show only that resource and the new output added:

```fish
tofu -chdir=infra/cluster plan
tofu -chdir=infra/cluster apply
kubectl get nodes -L pool -w          # a third node, pool=sandbox, Ready in a few minutes
```

Then check what the taint did:

```fish
set node (kubectl get nodes -l pool=sandbox -o name)
kubectl describe $node | grep -A2 Taints          # homelab/pool=sandbox:NoSchedule
kubectl get pods -A -o wide --field-selector spec.nodeName=(string replace node/ '' $node)
tofu -chdir=infra/cluster plan                    # no changes
```

- Expected on the node: Cilium, the CSI node plugin, konnectivity,
  node-exporter. Not Traefik, Alloy or kured.
- **Load balancer:** Traefik isn't on this node, so with
  `externalTrafficPolicy: Local` its health check fails there. In the Hub,
  the load balancer's backend should show the member as down and the two
  default nodes up, and `curl -sI https://hello.gronblom.dev` should keep
  answering 200 over a few dozen tries.

## 2. Install gVisor

```fish
kubectl apply -k docs/runbooks/sandbox-spike
kubectl -n sandbox-system rollout status ds/gvisor-installer
kubectl -n sandbox-system logs ds/gvisor-installer
```

The log starts with `fact` lines: keep them, they are answers 1 and 4.

- `fact containerd config:` the path and version it found. If the file isn't
  there, find the real one (`kubectl debug $node -it --image=alpine --
  chroot /host systemctl cat containerd`) and set `CONTAINERD_CONFIG` on the
  DaemonSet.
- `fact /dev/kvm:` and `fact cpu virtualisation flags`: Kata needs
  `present` and all CPUs.
- Then `installed gVisor ...` and `registered runsc in ... and restarted
  containerd`. The pod turns Ready when both are done.
- `error: containerd didn't come back with runsc registered; restoring ...`
  means the node rejected the config and has its old one back. Look at
  `journalctl -u containerd` on the node (`kubectl debug`, as above).

## 3. Run something under it

```fish
kubectl apply -f docs/runbooks/sandbox-spike/test.yaml
kubectl -n sandbox-spike wait pod/gvisor pod/runc --for=condition=Ready --timeout=5m
kubectl -n sandbox-spike logs gvisor      # Linux version 4.19.0-gvisor ...
kubectl -n sandbox-spike logs runc        # the node's own kernel
```

Both pods use public DNS resolvers, because ClusterIP services (cluster DNS
included) time out under gVisor here:
[gvisor-no-clusterip](../kb/networking/gvisor-no-clusterip.md). With the
cluster's resolver, `npm install` in the `gvisor` pod hangs without using
any CPU.

Two pods on the sandbox node, the same except for the runtime. The same
work in both gives the slowdown on UKS's disks (answer 3):

```fish
for p in runc gvisor
    echo "== $p"
    kubectl -n sandbox-spike exec $p -- bash -c '
        export PATH=/work/.npm-global/bin:$PATH
        time npm install -g --silent @anthropic-ai/claude-code
        claude --version
        mkdir -p t && cd t && npm init -y >/dev/null
        time npm install --silent typescript eslint vite
        time bash -c "find node_modules -type f -exec cat {} + | wc -c"'
end
```

`bash`, not `sh`: the image's `sh` is dash, which has no `time`
(`sh: 3: time: not found`, and every line after it fails too).

A real session, on the subscription. `claude setup-token` (on your machine)
prints a one-year token for model requests only. It goes through `kubectl
exec` and is stored nowhere in the cluster:

```fish
read -s -P 'token from claude setup-token: ' TOKEN
kubectl -n sandbox-spike exec gvisor -- env CLAUDE_CODE_OAUTH_TOKEN=$TOKEN \
    /work/.npm-global/bin/claude -p 'Write fizzbuzz.js in this directory, run it with node, and show the first 15 lines of output.' \
    --permission-mode acceptEdits --allowedTools 'Bash,Read,Write,Edit'
set -e TOKEN
```

These pods have no network policy: the session can reach the internet and
other app namespaces. Fine for a fizzbuzz, not for anything untrusted.

## 4. Hold a session to the hosts it needs

A session carries the subscription token, and runs whatever a prompt or a
file talks it into. `egress.yaml` leaves the pods in `sandbox-spike` only
DNS for, and port 443 of, `api.anthropic.com` and `registry.npmjs.org`. Its
header says what that does and doesn't stop.

Before, to see the difference:

```fish
kubectl -n sandbox-spike exec -i gvisor -- node - < docs/runbooks/sandbox-spike/egress-check.js
```

Then apply it and run the same check in both pods:

```fish
kubectl apply -f docs/runbooks/sandbox-spike/egress.yaml
kubectl -n sandbox-spike get cnp sandbox-egress     # VALID True
for p in runc gvisor
    echo "== $p"
    kubectl -n sandbox-spike exec -i $p -- node - < docs/runbooks/sandbox-spike/egress-check.js
end
```

Expected with the policy, in both pods (and what both printed on
2026-11-10):

```
dns api.anthropic.com                 <an address>
dns registry.npmjs.org                <an address>
https api.anthropic.com               HTTP 401
https registry.npmjs.org              HTTP 200
dns example.com                       ERR ...
tcp 1.1.1.1:443                       TIMEOUT
tcp 9.9.9.9:443                       TIMEOUT
tcp 10.96.0.1:443 (API Service)       TIMEOUT
```

- **The first four time out too:** Cilium's DNS proxy isn't answering for
  these pods (what the kind cluster did). Then rules by name need another
  design. Note whether it's both pods or only `gvisor`, and take the policy
  off again (below).
- **They work under `runc` and not under `gvisor`:** the proxy doesn't work
  for gVisor's network stack. Same conclusion, for gVisor only.

If the check passes, run the session of step 3 again with the policy on.
Claude Code also tries hosts that aren't allowed (telemetry, update
checks); they're optional, and this shows whether it minds. It didn't: the
session ran through on 2026-11-10. To see what was dropped while it ran,
in another terminal:

```fish
set node (kubectl get nodes -l pool=sandbox -o jsonpath='{.items[0].metadata.name}')
set cilium (kubectl -n kube-system get pods -l k8s-app=cilium --field-selector spec.nodeName=$node -o name)
kubectl -n kube-system exec $cilium -c cilium-agent -- cilium-dbg monitor --type drop --type l7
```

To take the policy off: `kubectl delete -f docs/runbooks/sandbox-spike/egress.yaml`.

## 5. Does it stay?

- Leave the installer running for a day. A second `registered runsc` line
  in its log means something on the node removed the entry (the installer
  put it back; note when).
- Restart the node (Hub: the server, Restart), then check the installer's
  log and start the `gvisor` pod again (`kubectl -n sandbox-spike delete pod
  gvisor`, then apply `test.yaml`). Done on 2026-11-10: the new log had
  only `fact` lines (nothing to install or register again), and the pod
  started.

## 6. Write down the results

In [sandbox-gvisor-on-uks](../kb/ops/sandbox-gvisor-on-uks.md) (the `kb`
skill), which has everything up to the restart: add what the installer's
log says after a day or more, and the choice that follows.

## Clean up

```fish
kubectl delete -f docs/runbooks/sandbox-spike/test.yaml     # the namespace, its pods and the egress policy
kubectl delete -k docs/runbooks/sandbox-spike
```

`runsc` and the config entry stay on the node until it's replaced; they do
nothing without the RuntimeClass. To stop paying for the node, remove
`upcloud_kubernetes_node_group.sandbox` and the `sandbox_node_group_name`
output from `infra/cluster` and apply.

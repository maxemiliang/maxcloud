# Agent sessions in a sandbox: running the platform side

Users run Claude Code sessions with `mcloud sandbox`
([the guide](../../../docs/sandbox.md)). This page is for whoever runs the
cluster: what's behind it, and what to do when it doesn't work. Why it's
built this way is in
[agent-sessions-sandbox](../kb/ops/agent-sessions-sandbox.md).

Commands are fish, run from `platform/`, as an admin.

## What's there

- **The `sandbox` node group** (`infra/cluster/nodes.tf`, applied with
  tofu, not by Flux): one tainted node, labelled `pool=sandbox`. Without a
  Ready node there, the installer has nothing to run on and sessions stay
  Pending.
- **`sandbox-controllers`** (Flux Kustomization, `sandbox/controllers`):
  the gVisor installer DaemonSet on the `sandbox` node group, the `gvisor`
  RuntimeClass, and the agent-sandbox controller.
- **`sandbox-sessions`** (`sandbox/sessions`): the ResourceSet
  `maxcloud-sandboxes`, which makes a namespace `sbx-<user>` for every
  sandbox entry; the role a user gets there; the admission rules for
  entries and for Secrets in those namespaces.
- **An entry per user:** a ResourceSetInputProvider `sandbox-<user>` in
  `flux-system`, labelled `run.gronblom.dev/sandbox`. mcloud makes it the
  first time a user runs `mcloud sandbox`; it isn't in git.
- **A session:** a SandboxClaim in the user's namespace, and the pod of the
  same name agent-sandbox makes for it. The user's token is the Secret
  `claude-code` there.

```fish
kubectl -n flux-system get resourcesetinputproviders -l run.gronblom.dev/sandbox=true
kubectl get sandboxclaims -A
kubectl get pods -A -l agents.x-k8s.io/sandbox-name-hash -o wide
```

`mcloud sandbox ... --user <name>` does for another user what it does for
you: list and stop their sessions, remove their sandbox.

## Check what a session can reach

A session reaches the internet and nothing in the cluster. Check it after
a change to the policy in `sandbox/sessions/users.yaml`, to Cilium, or to
gVisor. In a session of your own, with a node's kubelet and a pod (CoreDNS
here) as the things that must not answer:

```fish
set kubelet (kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'):10250
set pod (kubectl -n kube-system get pods -l k8s-app=kube-dns -o jsonpath='{.items[0].status.podIP}'):53
mcloud sandbox shell <session> -- node - $kubelet $pod < docs/runbooks/sandbox-sessions/egress-check.js
```

```
dns api.anthropic.com                 <an address>
https api.anthropic.com               HTTP 401
https registry.npmjs.org              HTTP 200
https github.com                      HTTP 200
tcp github.com:22                     connected
tcp 169.254.169.254:80 (metadata)     TIMEOUT
tcp 10.96.0.1:443 (API Service)       TIMEOUT
tcp <node address>:10250              TIMEOUT
tcp <pod address>:53                  TIMEOUT
```

`connected` in the last four lines means the policy isn't holding that
namespace (`kubectl -n sbx-<user> get cnp sandbox-egress`, VALID should be
True). A failure in the first five means it allows less than it should.
To watch what's dropped while a session runs:

```fish
set node (kubectl get nodes -l pool=sandbox -o jsonpath='{.items[0].metadata.name}')
set cilium (kubectl -n kube-system get pods -l k8s-app=cilium --field-selector spec.nodeName=$node -o name)
kubectl -n kube-system exec $cilium -c cilium-agent -- cilium-dbg monitor --type drop
```

Sessions leave through the NAT gateway, so what one does on the internet
comes from the cluster's address.

## Let a user run more sessions

Two by default, and the node holds four in all. Only an admin may set it:

```fish
kubectl -n flux-system patch resourcesetinputprovider sandbox-<user> --type merge \
    -p '{"spec":{"defaultValues":{"sessions":"3"}}}'
```

The quota in `sbx-<user>` follows within ten minutes.

## A new Claude Code version

Two merges, because the cluster runs the image tag named in
`sandbox/sessions/users.yaml`:

1. Change `CLAUDE_CODE_VERSION` in `.github/workflows/sandbox-image.yaml`
   (Renovate offers it). Merging builds and pushes
   `ghcr.io/maxemiliang/maxcloud-sandbox:<version>-<revision>`. A change to
   the Dockerfile alone needs `IMAGE_REVISION` raised instead.
2. Name the new tag in `users.yaml` (Renovate offers that too, once the tag
   exists).

Running sessions keep the image they started with.

The same two merges add or update a tool in the image: the Dockerfile has
the tools (Go and uv are images of their own, `gh` and pnpm are `ARG`s;
Renovate offers all four), and the workflow's "Run it" step checks them.
Raise `IMAGE_REVISION`. What's in it, and why not more:
[the knowledge-base entry](../kb/ops/sandbox-image-toolset.md). The image is
about 2.2 GB (580 MB compressed), and the sandbox node pulls it the first
time a session starts on it: keep it from growing past 2.5 GB.

## When it doesn't work

mcloud says most of these itself; this is what's underneath.

- **`mcloud sandbox start` waits for the sandbox and gives up:** the
  ResourceSet didn't make the namespace.
  `kubectl -n flux-system describe resourceset maxcloud-sandboxes` says
  why; one entry it can't render stops every user's.
- **A user with dots, capitals or an `@` in their Authelia username can't
  have a sandbox:** the name becomes a namespace. They need another
  username.
- **The pod is in `CreateContainerConfigError`:** the user has no token
  (`mcloud sandbox token set`).
- **`ImagePullBackOff`:** the tag hasn't been built (Actions,
  `sandbox-image`), or the package isn't public: the cluster pulls from
  GHCR without credentials. Its visibility is under
  https://github.com/users/maxemiliang/packages/container/maxcloud-sandbox/settings.
- **The pod stays in `ContainerCreating`, with `no runtime for "runsc" is
  configured` in its events:** the node has no gVisor yet. The installer
  puts it there within seconds of a node joining, and the pod then starts
  by itself; if it doesn't, `kubectl -n sandbox-system logs ds/gvisor-installer`.
- **`start` says `2 of 2 sessions are running`:** the user is at their
  limit (the ResourceQuota `sessions` in their namespace). A claim made
  past it some other way says `ReconcilerError`, and its Sandbox's events
  say `exceeded quota: sessions`.
- **`start` says the sandbox node has no room:** four sessions are running
  already, everyone's counted. mcloud waits half a minute for room, then
  removes the session it made.
- **The pod is `Pending` otherwise:** no sandbox node is Ready (`kubectl
  describe pod`).
- **`mcloud sandbox pull` says the directory isn't a git repository:** the
  session wasn't started `--from` one; `--dir` names the repository in it.
- **Claude Code says `Failed to authenticate. API Error: 401`:** the user's
  token is wrong or more than a year old.
- **`npm install`, `git clone` or a web fetch fails in a session:** the
  policy allows them, so check it as above. A private repository fails
  because a session has no git credentials, not because of the network.
- **A session can't reach an app or a database in the cluster:** that's the
  policy, and meant.
- **After a cluster rebuild nobody has a sandbox:** entries and tokens
  aren't backed up. Users run `mcloud sandbox token set` again.

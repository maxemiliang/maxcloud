# Agent sessions in a sandbox: running the platform side

Users run Claude Code sessions with `mcloud sandbox`
([the guide](../../../docs/sandbox.md)). This page is for whoever runs the
cluster: what's behind it, and what to do when it doesn't work. Why it's
built this way is in
[agent-sessions-sandbox](../kb/ops/agent-sessions-sandbox.md).

Commands are fish, run from `platform/`, as an admin.

## What's there

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

After a change to the policy in `sandbox/sessions/users.yaml`, to Cilium,
or to gVisor. In a session of your own:

```fish
mcloud sandbox shell <session> -- node - < docs/runbooks/sandbox-sessions/egress-check.js
```

```
dns api.anthropic.com                 <an address>
https api.anthropic.com               HTTP 401
dns registry.npmjs.org                ERR EAI_AGAIN
dns example.com                       ERR EAI_AGAIN
tcp 1.1.1.1:443                       TIMEOUT
tcp 9.9.9.9:443                       TIMEOUT
tcp 10.96.0.1:443 (API Service)       TIMEOUT
```

Anything else in the last five lines means the policy isn't holding that
namespace (`kubectl -n sbx-<user> get cnp sandbox-egress`, VALID should be
True). To watch what's dropped while a session runs:

```fish
set node (kubectl get nodes -l pool=sandbox -o jsonpath='{.items[0].metadata.name}')
set cilium (kubectl -n kube-system get pods -l k8s-app=cilium --field-selector spec.nodeName=$node -o name)
kubectl -n kube-system exec $cilium -c cilium-agent -- cilium-dbg monitor --type drop --type l7
```

A host that sessions should reach is added to both rules of that policy,
for every user at once. Its comment says what each added host costs.

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
- **The claim says `ReconcilerError`:** the user is at their limit. The
  Sandbox's events say `exceeded quota: sessions`.
- **The pod is `Pending`:** no sandbox node is Ready, or four sessions are
  running already (`kubectl describe pod`).
- **Claude Code says `Failed to authenticate. API Error: 401`:** the user's
  token is wrong or more than a year old.
- **`npm install`, `git clone` or a web fetch fails in a session:** that's
  the policy, not a fault.
- **After a cluster rebuild nobody has a sandbox:** entries and tokens
  aren't backed up. Users run `mcloud sandbox token set` again.

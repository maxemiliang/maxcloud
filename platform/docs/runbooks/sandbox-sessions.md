# Agent sessions in a sandbox

A session is one pod with Claude Code in it, under gVisor on the `sandbox`
node group. It logs in with the Claude subscription token and can reach the
Claude API and nothing else: no other site, no package registry, nothing in
the cluster. The pieces are in `sandbox/` (`clusters/personal/sandbox.yaml`
applies them), and why it's built this way is in
[agent-sessions-sandbox](../kb/ops/agent-sessions-sandbox.md).

Only cluster admins can make one. At most four run at a time
(`sandbox/sessions/quota.yaml`).

Commands are fish, run from `platform/`.

## Start one

```fish
set s (kubectl create -f docs/runbooks/sandbox-sessions/claim.yaml -o name | string replace -r '.*/' '')
kubectl -n sandbox wait sandboxclaim/$s --for=condition=Ready --timeout=3m
kubectl -n sandbox exec -it $s -- claude
```

The claim, its Sandbox and its pod all have the same name (`session-xxxxx`).
The first session on a node waits for the image (about a gigabyte); after
that a pod is up in seconds.

A single prompt, without the interface:

```fish
kubectl -n sandbox exec $s -- claude -p 'Write fizzbuzz.js, run it with node, and show the first 15 lines.' \
    --permission-mode acceptEdits --allowedTools 'Bash,Read,Write,Edit'
```

Files go in and out with `kubectl cp` (the session can't clone or push
anything itself). The work directory is `/work`:

```fish
kubectl -n sandbox cp ./some-repo $s:/work/some-repo
kubectl -n sandbox cp $s:/work/some-repo ./some-repo-after
```

`/work` is the session's home too, so Claude Code's own history is there
and `claude --resume` works for as long as the pod exists.

## End one

```fish
kubectl -n sandbox get sandboxclaims
kubectl -n sandbox delete sandboxclaim $s
```

Deleting the claim deletes the pod and everything in `/work`. Copy out what
matters first. Nothing ends a session by itself: to give one an end time,

```fish
kubectl -n sandbox patch sandboxclaim $s --type merge -p \
    '{"spec":{"lifecycle":{"shutdownPolicy":"Delete","shutdownTime":"'(date -u -d '+8 hours' +%Y-%m-%dT%H:%M:%SZ)'"}}}'
```

## Check what a session can reach

```fish
kubectl -n sandbox exec -i $s -- node - < docs/runbooks/sandbox-sessions/egress-check.js
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

Anything else in the last five lines means the policy isn't holding the
namespace (`kubectl -n sandbox get cnp sandbox-egress`, VALID should be
True). To watch what's dropped while a session runs:

```fish
set node (kubectl get nodes -l pool=sandbox -o jsonpath='{.items[0].metadata.name}')
set cilium (kubectl -n kube-system get pods -l k8s-app=cilium --field-selector spec.nodeName=$node -o name)
kubectl -n kube-system exec $cilium -c cilium-agent -- cilium-dbg monitor --type drop --type l7
```

A session that needs another host gets it in `sandbox/sessions/network.yaml`,
whose header says what each added host costs.

## Set or renew the subscription token

`claude setup-token` (on your machine, logged in to the subscription)
prints a token that is good for a year and for model requests only. It
lives in the Secret `claude-code` in `sandbox`, from the private repo. With
the `private/` submodule checked out:

```fish
set out ../private/secrets/sandbox/claude-code.sops.yaml
mkdir -p (dirname $out)
read -s -P 'token from claude setup-token: ' TOKEN
printf '%s\n' "CLAUDE_CODE_OAUTH_TOKEN=$TOKEN" \
  | kubectl -n sandbox create secret generic claude-code \
      --from-env-file=/dev/stdin --dry-run=client -o yaml \
  | sops encrypt --filename-override $out > $out
set -e TOKEN
```

The first time, add `sandbox/claude-code.sops.yaml` to
`private/secrets/kustomization.yaml`. Commit and push in `private/`. New
sessions get the new token; a running one keeps the token it started with.

## A new Claude Code version

Two merges, because the cluster runs the image tag named in
`sandbox/sessions/claude-code.yaml`:

1. Change `CLAUDE_CODE_VERSION` in `.github/workflows/sandbox-image.yaml`
   (Renovate offers it). Merging builds and pushes
   `ghcr.io/maxemiliang/maxcloud-sandbox:<version>-<revision>`. A change to
   the Dockerfile alone needs `IMAGE_REVISION` raised instead.
2. Name the new tag in `claude-code.yaml` (Renovate offers that too, once
   the tag exists).

Running sessions keep the image they started with.

## When it doesn't work

- **The pod is in `CreateContainerConfigError`:** the Secret `claude-code`
  isn't there. Set the token (above).
- **`ImagePullBackOff`:** the tag hasn't been built (Actions,
  `sandbox-image`), or the package isn't public: the cluster pulls from
  GHCR without credentials. Its visibility is under
  https://github.com/users/maxemiliang/packages/container/maxcloud-sandbox/settings.
- **The pod stays in `ContainerCreating`, with `no runtime for "runsc" is
  configured` in its events:** the node has no gVisor yet. The installer
  puts it there within seconds of a node joining, and the pod then starts
  by itself; if it doesn't, `kubectl -n sandbox-system logs ds/gvisor-installer`.
- **The claim says `ReconcilerError`:** four sessions are running already.
  The Sandbox's events say `exceeded quota: sessions`.
- **The pod is `Pending`:** no sandbox node is Ready, or it's full
  (`kubectl describe pod`).
- **Claude Code says `Failed to authenticate. API Error: 401`:** the token
  is wrong or more than a year old. Renew it (above).
- **`npm install`, `git clone` or a web fetch hangs or fails in a session:**
  that's the policy, not a fault.

---
title: SSH into agent sessions is sshd -i over the exec channel, started by mcloud per connection
kind: decision
area: ops
tags: [sandbox, ssh, sshd, mcloud, exec, rbac, gvisor, port-forward]
learned: 2026-11-11
updated: 2026-11-11
where:
  - sandbox/image/Dockerfile
  - ../internal/kube/sandbox_ssh.go
  - ../internal/cli/sandbox_ssh.go
  - docs/runbooks/sandbox-sessions.md
---

# SSH into agent sessions is sshd -i over the exec channel, started by mcloud per connection

**Decision:** `mcloud sandbox ssh-proxy` is ssh's ProxyCommand. For each
connection it execs a script in the session that runs `/usr/sbin/sshd -i`
as the session's user, and carries the SSH protocol over the exec's stdin
and stdout. `mcloud sandbox ssh` runs ssh that way; `mcloud sandbox
ssh-config` gives plain ssh, scp, rsync and editors a host name per
session (`SESSION.sbx`, `SESSION.USER.sbx`). The image has `sshd` and
`rsync`, and the `node` user's home is `/work`.

**Why:**

- **Nothing new reaches a session.** No Service, no port, no listener, no
  network policy change, no RBAC change: it's `pods/exec`, which users
  have in their own namespace and admins everywhere. So the access rule
  is the old one, by construction, and a connection needs an mcloud
  login.
- **The settings are on sshd's command line**, from mcloud, not in a file
  in the session where the agent could change them for the next
  connection: logins only by key and only as `node`, no PAM, no agent, X11,
  socket or remote (`-R`) forwarding, `-L` only to the session's own
  loopback (`PermitOpen localhost:* 127.0.0.1:* [::1]:*`). The client side
  says the same (`ForwardAgent no`, `IdentityAgent none`, ...): a
  forwarded agent in a session is a credential the agent there could use.
- **Host keys without trusting on first use.** The script prints the
  session's host public key before it starts sshd; ssh-proxy takes that
  first line off the stream, accepts exactly one ed25519 key (no options,
  no `@cert-authority`, nothing else), re-encodes it and writes it to a
  known_hosts file of that host name's alone before any of sshd's bytes
  reach ssh, which runs with `StrictHostKeyChecking yes`. The key comes
  over the already-authenticated API channel, so a key ssh hasn't seen
  isn't a question to the user.
- **The host name is quoted in ProxyCommand** (`'%n'`), and the `Host`
  line leaves out names with a quote in them (`"!*'*"`): ssh hands
  ProxyCommand to a shell with the name put in as it is, and OpenSSH
  before 9.6 doesn't refuse names with shell syntax (CVE-2023-51385, a
  submodule URL in a hostile repository). Debian's and recent OpenSSH
  refuse them anyway (`hostname contains invalid characters`).
- **mcloud's own client key** (in mcloud's config directory), never the
  user's: only its public half goes into the session's
  `authorized_keys`, which ssh-proxy fills on every connection.
- **Unprivileged sshd works** as uid 1000 with every capability dropped
  and no privilege escalation (OpenSSH 9.2, Debian bookworm; tested with
  Docker and k3s, not yet under gVisor): sshd skips its privilege
  separation chroot when it isn't root, and `sshd -i` needs no listening
  socket. Two settings it needs here:
  `StrictModes no`, because the work volume (an emptyDir) is
  world-writable and sshd refuses keys under such a home; and the
  session's environment passed with `SetEnv`, because sshd gives a login a
  fresh one, without the token or `NPM_CONFIG_PREFIX`. Values with spaces
  or quotes stay out (they'd break the option).
- **Home is `/work`** in `/etc/passwd`: sshd starts a login in the home
  directory from there, not `$HOME`, and the pod's `HOME` was already
  `/work`.
- **PATH twice:** plain `ssh SESSION.sbx` is a login shell, and Debian's
  `/etc/profile` replaces PATH with its own, without what the image's
  `ENV` puts on it. The script passes the session's PATH as
  `MCLOUD_SSH_PATH` too, and `/etc/profile.d/sandbox-ssh-path.sh` in the
  image puts it back.

**Rejected:**

- **`pods/portforward` to an sshd listening in the pod:** a new RBAC rule
  for every user, and a long-running sshd in every session. Unproven under
  gVisor here, too. Exec already carries a byte stream.
- **A Service, an ingress route or a LoadBalancer:** a way in from the
  network, which sessions have never had; the quota has zero
  LoadBalancers and NodePorts on purpose.
- **The keys as Secrets:** admission allows the token and nothing else in
  a sandbox namespace, and the session can read whatever it mounts.
- **The user's own SSH key or agent in the session:** the agent there can
  read everything, and sessions reach the internet.
- **Trust on first use** (`StrictHostKeyChecking accept-new`): mcloud can
  read the key over the authenticated channel, so it has no reason to ask.
- **`KnownHostsCommand`:** a second exec for every connection; the first
  line of the one exec does the same.
- **An sshd config file baked into the image:** settings mcloud owns would
  then change only with a new image, and the CLI and image would have to
  agree on its path. The script runs `sshd -f /dev/null` with every
  setting given.

**What it doesn't give:** what comes out over scp or rsync (links,
`.git`) isn't filtered like `mcloud sandbox cp`.

**Revisit when:** exec is too slow for what goes over SSH (portforward,
measured under gVisor), an editor needs a socket forward, or sessions get
previews of their own.

See also: [agent-sessions-sandbox](agent-sessions-sandbox.md),
[sshd-terminal-tty-group-gvisor](sshd-terminal-tty-group-gvisor.md).

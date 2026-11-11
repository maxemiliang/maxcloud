---
title: Under gVisor, sshd as a plain user can't hand a terminal to group tty unless the user is in it
kind: gotcha
area: ops
tags: [sandbox, gvisor, ssh, sshd, pty, devpts, mcloud]
learned: 2026-11-11
updated: 2026-11-11
where:
  - sandbox/sessions/users.yaml
---

# Under gVisor, sshd as a plain user can't hand a terminal to group tty unless the user is in it

**Symptom:** `mcloud sandbox ssh` (or `ssh SESSION.sbx`) into a session on
the cluster connects, then:

```
chown(/dev/pts/0, 1000, 5) failed: Operation not permitted
Connection to wow.alice.sbx closed by remote host.
```

The same image and the same command work in Docker and on a k3s or kind
cluster.

**Cause:** when a login asks for a terminal, sshd makes the terminal's
device belong to the user and to group `tty` (5), mode 620, and treats a
failure as fatal. With runc it has nothing to do: `/dev/pts` is mounted
with `gid=5,mode=620`, so a new terminal is born that way. gVisor mounts
its own devpts without those options (`none on /dev/pts type devpts
(rw,nosuid,noexec)`), so a new terminal belongs to the group of whoever
opened it, 1000 here, mode 600. sshd then calls `chown`, and a user who
isn't root may only give a file to a group they are in. The session's user
was in group 1000 alone.

**Fix:** the session template has `supplementalGroups: [5]`, so the
session's user is in `tty` and the `chown` is allowed. Nothing else
changes: it's a container with one user in it.

Not chosen: removing group `tty` from the image, so that sshd asks for the
user's own group and has nothing to change. It works by an accident of
sshd's code, and other programs expect the group.

**Recognise it:** in a session,
`mcloud sandbox shell SESSION -- id` shows `groups=1000(node)` without
`5(tty)`. Connections that ask for no terminal (`scp`, `rsync`,
`mcloud sandbox forward`) never hit it.

See also: [agent-sessions-ssh](agent-sessions-ssh.md),
[sandbox-gvisor-on-uks](sandbox-gvisor-on-uks.md).

# Agent sessions in a sandbox

`mcloud sandbox` runs Claude Code in the cluster instead of on your
machine: each session is a pod of its own, under gVisor, logged in with
your Claude subscription. A session can reach the Claude API and nothing
else: no other site, no package registry, nothing in the cluster. That
makes it a place to let an agent work unattended on code you copy in.

```fish
mcloud sandbox token set            # once: your Claude token
mcloud sandbox start refactor       # a session named refactor
mcloud sandbox cp ./my-repo refactor:my-repo
mcloud sandbox claude refactor      # Claude Code, in the session
mcloud sandbox cp refactor:my-repo ./my-repo-after
mcloud sandbox stop refactor        # ends it; its files go with it
```

You need to be logged in (`mcloud login`) as a member of
`maxcloud-developers` or an admin. The first `mcloud sandbox` command makes
your sandbox: a namespace of your own that holds your token and your
sessions. Nobody else's commands reach it.

## The token

Sessions use your Claude subscription, not an API key. On your own
machine, logged in to Claude:

```fish
claude setup-token
```

It prints a token for model requests only, good for a year. Give it to
`mcloud sandbox token set` (a hidden prompt, or stdin). It is stored in
your sandbox in the cluster, in no repository, and only your sessions and
the cluster's admins can read it. A session reads it when it starts, so
after renewing it, start new sessions. `mcloud sandbox token unset`
removes it.

The token is in the session's environment, where the agent could read it.
That is why a session can reach only the Claude API: there is nowhere
else to send it.

## Sessions

- `mcloud sandbox start [NAME]` starts one and waits until it's up
  (seconds; the first on a node pulls a gigabyte of image first).
  `--for 8h` ends it by itself after that long.
- `mcloud sandbox list` shows yours.
- `mcloud sandbox claude [SESSION]` is Claude Code in the session's work
  directory, `/work`. What follows `--` goes to `claude`:
  `mcloud sandbox claude -- --continue`, or a single prompt with
  `mcloud sandbox claude -- -p 'Run the tests and fix what fails' --permission-mode acceptEdits`.
  Claude Code keeps its conversations in the session.
- `mcloud sandbox shell [SESSION]` is a shell there, or one command after
  `--`.
- `mcloud sandbox stop SESSION...` (or `--all`) ends sessions.

SESSION can be left out when you have one session. You can have two
running at a time; an admin can raise that.

The image has Node 24 and git. A session can't install packages or clone
anything itself: bring what the work needs with `cp`, `node_modules`
included.

## Files

`/work` exists for as long as the session does, and is gone when it stops.
`mcloud sandbox cp` copies a file or a directory in or out, like `cp`: the
copy goes into the destination if that is a directory, and gets its name
otherwise. Paths in a session are relative to `/work`.

```fish
mcloud sandbox cp ./my-repo refactor:            # /work/my-repo
mcloud sandbox cp refactor:my-repo/report.md .
```

Coming out of a session, only files and directories are copied. Links and
special files are left out, and nothing is written outside the destination,
whatever the session's files are named.

## Removing your sandbox

`mcloud sandbox remove` deletes your sandbox: every session, their files
and your token. The next `mcloud sandbox start` makes a new one.

Reference: [mcloud sandbox](reference/cli/mcloud-sandbox.md).

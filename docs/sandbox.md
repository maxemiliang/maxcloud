# Agent sessions in a sandbox

`mcloud sandbox` runs Claude Code in the cluster instead of on your
machine: each session is a pod of its own, under gVisor, logged in with
your Claude subscription. A session can reach the internet (to clone,
install packages, look things up) and nothing in the cluster. That makes
it a place to let an agent work unattended, away from your machine and
your credentials.

```fish
mcloud sandbox token set                     # once: your Claude token
cd my-repo
mcloud sandbox start refactor --from .       # a session with this repository in it
mcloud sandbox prompt 'Fix the failing tests, and commit'
mcloud sandbox pull                          # its commits, into your repository
mcloud sandbox stop refactor                 # ends it; its files go with it
```

`mcloud sandbox claude` is Claude Code itself in the session, for working
with it instead of handing it one task.

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

The token is in the session's environment, where the agent can read it,
and a session can reach the internet. An agent that is talked into it, by
a web page or by a file in a repository, could send the token or the code
in the session somewhere else. Keep that in mind when you choose what a
session works on and what it reads, and put no other credentials in a
session that you wouldn't hand to the agent.

## Sessions

- `mcloud sandbox start [NAME]` starts one and waits until it's up
  (seconds; the first on a node pulls the image first, a couple of
  gigabytes, and says so). `--from DIR` copies a directory of yours into it, and makes that
  where the session's commands start. `--for 8h` ends it by itself after
  that long.
- `mcloud sandbox list` shows yours, and how many you may have.
- `mcloud sandbox prompt [SESSION] 'TASK'` gives Claude Code one task and
  ends when the task does. What it does shows as it goes; its answer comes
  last, on stdout. `--continue` carries on from the last conversation.
- `mcloud sandbox claude [SESSION]` is Claude Code itself. What follows
  `--` goes to `claude`: `mcloud sandbox claude -- --continue`. Claude Code
  keeps its conversations in the session.
- `mcloud sandbox shell [SESSION]` is a shell there, or one command after
  `--`.
- `mcloud sandbox stop SESSION...` (or `--all`) ends sessions.

SESSION can be left out when you have one session. `prompt`, `claude` and
`shell` run in the directory the session was started `--from` (or `--dir`),
and in `/work` otherwise.

You can have two sessions running at a time; an admin can raise that. The
sandbox node holds four in all, everyone's counted: when it's full, `start`
says so and gives up.

### What a task may do

Nobody answers questions during `prompt`, so `--permission-mode` says what
Claude Code may do without asking. The default is `auto`: it edits files
and runs commands, and Claude Code checks each action itself first.
`acceptEdits` allows edits and no commands; `bypassPermissions` allows
everything, unchecked.

## What's in a session

A session is a Linux machine (Debian 12) with a coding machine's usual
tools, but without root: no `sudo`, no `apt`, no Docker. What a session needs
it installs for itself, under `/work`.

- **Languages and package managers:** Node 24 with npm, pnpm and yarn;
  Python 3.11 with pip, venv and uv; Go.
- **Build tools:** gcc, g++, make, cmake, ninja and pkg-config, with the
  headers for OpenSSL, libffi, SQLite and zlib, so packages that compile
  native code build.
- **Version control and search:** git, `gh` (without a login, so for public
  repositories), ripgrep (`rg`), `fd`, `jq`, `sqlite3`, `tree`.
- **Other:** curl, wget, zip, unzip, xz, rsync, `nano` and `vi`.
- **Claude Code**, at the version the image names.

Not there, and on demand: Rust, bun, deno, Java and the rest. A session has
the internet and can install them for itself, and what it installs lands in
`/work` and on `PATH`: `curl https://sh.rustup.rs | sh -s -- -y --profile
minimal` for Rust, `npm install -g bun`, `pip install --user` or `uv tool
install` for Python tools, `go install`, `cargo install`. Dependencies of the
project (`npm install`, `pip install`, `go mod download`) work the same way.

It has no git credentials: a private repository goes in with `start --from`
or `cp`. Nothing in the cluster is reachable from a session, your apps
included, and nothing can connect to one.

## Getting commits back

`mcloud sandbox pull [SESSION]`, in your repository, fetches what the
session has committed. Each of its branches arrives as
`sandbox/SESSION/BRANCH`, and the branch you're on moves forward to the
session's branch of the same name when that is a fast-forward
(`--no-merge` only fetches). Then it's your git: push, open a pull
request. The session never has your git credentials.

```fish
mcloud sandbox pull refactor
git push                      # or: gh pr create
```

Only commits come over. `pull` says when the session has changes it
hasn't committed; ask for a commit
(`mcloud sandbox prompt 'Commit your work' --continue`) or copy the files
out. For a session that wasn't started `--from` a directory, name the
repository in it with `--dir`.

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
whatever the session's files are named. Git's own files (`.git`) are left
out as well: hooks and configuration in them would run on your machine the
next time you use git there. A session's commits come with `pull`.

## Removing your sandbox

`mcloud sandbox remove` deletes your sandbox: every session, their files
and your token. The next `mcloud sandbox start` makes a new one.

Reference: [mcloud sandbox](reference/cli/mcloud-sandbox.md).

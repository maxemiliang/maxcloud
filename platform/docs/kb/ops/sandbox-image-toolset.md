---
title: The sandbox image carries what a session can't install itself, modelled on boat.dev's, at about 2.2 GB
kind: decision
area: ops
tags: [sandbox, claude-code, image, ghcr, gvisor, boat]
learned: 2026-11-11
updated: 2026-11-11
where:
  - sandbox/image/Dockerfile
  - ../.github/workflows/sandbox-image.yaml
  - sandbox/sessions/users.yaml
  - docs/runbooks/sandbox-sessions.md
---

# The sandbox image carries what a session can't install itself, modelled on boat.dev's, at about 2.2 GB

**Decision:** the image has the toolset of a general-purpose coding
machine, like the sandboxes of [boat.dev](https://boat.dev), as far as a
session without root can use it and one small node (2 CPU, 4 GB, sessions
limited to 1.5 GB) can pull it. It was 975 MB (Node, git, Claude Code); it is
2.2 GB (about 280 MB and 580 MB compressed, which is what the node pulls
the first time).

boat's list is from [Base image and pre-installed tools](https://docs.boat.dev/machines/tools)
(Ubuntu 24.04; measured by them 2026-10-13). It doesn't say what
editors it has, so `nano` and `vim-tiny` are a guess from what devcontainers'
universal image and Codespaces carry.

**In the image:**

- **Node 24** (the base), npm, **pnpm**, yarn 1; **Python 3.11** with pip
  and venv, **uv**; **Go**. Go and uv are copied from their official images,
  so Renovate keeps their tags and digests current; pnpm and `gh` are
  `ARG`s with a `# renovate:` comment.
- **gcc, g++, make, cmake, ninja, pkg-config**, and the headers native
  builds ask for (OpenSSL, libffi, SQLite, zlib, Python's): without root a
  session can't add them, and they are what `node-gyp`, pip wheels and
  cargo's `-sys` crates need.
- **git, gh, ripgrep, fd, jq, sqlite3, curl, wget, zip, unzip, xz, tree,
  less, nano, vim-tiny, rsync, file, procps.**

**Left out, and why:**

- **Rust** (boat has it): 645 MB for `rustc`, `cargo`, `clippy` and `rustfmt`
  alone, and the biggest single thing. A session installs it in about
  twenty seconds, for itself: `curl https://sh.rustup.rs | sh -s -- -y
  --profile minimal` needs only `cc`, which the image has, and lands in
  `/work/.cargo/bin`, which is on PATH. That also gives it the toolchain a
  repository's `rust-toolchain.toml` asks for, which a toolchain baked in
  at `/usr/local` could not (rustup can't write there).
- **bun and deno** (bun alone is 107 MB): `npm i -g bun`, or their
  installers, put them under `/work`, whose bin directories are on PATH.
- **JVM languages, .NET, Ruby, PHP, Erlang/Elixir, R:** each is a few
  hundred MB, and a JVM or .NET build in 1.5 GB of memory is a poor fit.
  Toolchain managers that don't need root (SDKMAN, `uv python`) do these
  on demand.
- **Docker** and anything nested: a session has no privileges, under gVisor.
- **Chrome, ffmpeg, a desktop (`xdotool`, `wmctrl`):** hundreds of MB for
  what a session with no display rarely needs, and a browser under gVisor
  as uid 1000 is untried here.
- **The other coding agents** (Codex, OpenCode, Cursor CLI, ...): a
  session is Claude Code, logged in with the user's token; the others would
  need credentials of their own in a session that can reach the internet.
- **sudo, apt at run time:** by design. If a tool is missing, it goes in
  the image.

**PATH:** the image sets it, not `users.yaml` (an `env: PATH` there would
replace the image's). `/work/.local/bin` (pip `--user`, `uv tool`),
`/work/.npm-global/bin`, `/work/.cargo/bin`, `/work/go/bin`, `/work/.bun/bin`
and `/work/.deno/bin` come before the system's, so a tool a session
installs for itself wins over the image's, as on a machine of your own.
`pip` is told it may install outside a venv (`PIP_BREAK_SYSTEM_PACKAGES`,
Debian's PEP 668 marker): as uid 1000 it can only write under `HOME`.

**Revisit when:** the image goes much past 2.5 GB (what the first start on a
node costs), sessions need Rust or a JVM often enough that twenty seconds
of install is the wrong price, or the node grows.

See also: [agent-sessions-sandbox](agent-sessions-sandbox.md).

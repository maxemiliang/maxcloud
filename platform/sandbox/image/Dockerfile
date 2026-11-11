# The image agent sessions run in (sandbox/sessions/users.yaml): Claude Code
# and the toolset of a general-purpose coding machine. Built and pushed to
# ghcr.io/maxemiliang/maxcloud-sandbox by .github/workflows/sandbox-image.yaml,
# which also sets the Claude Code version.
#
# A session is not root, and has no sudo, no apt and no Docker: whatever
# needs root is in the image. What doesn't (project dependencies, a language
# toolchain or its version) a session fetches itself: it has the internet,
# and HOME is an empty volume at /work. So every tool is installed
# system-wide, and the per-user bin directories under /work are on PATH for
# whatever a session installs for itself.
#
# What's in it, and what was left out and why: docs/kb/ops/sandbox-image-toolset.md.

# Go comes as an image of its own, and is copied out of it: Renovate keeps its
# tag and digest current, as for the base image.
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS go
FROM ghcr.io/astral-sh/uv:0.13.7@sha256:96f40d3904aad8d0bd010539001d36c8e2188b981e1334471a90c4a1d93bb490 AS uv

FROM node:24-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9

# Compilers and headers for what builds native code (npm's node-gyp, pip
# wheels, cargo's -sys crates, which need cc to link as well), Python, and
# the command-line tools an agent reaches for.
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    build-essential cmake ninja-build pkg-config \
    libffi-dev libsqlite3-dev libssl-dev zlib1g-dev \
    python3 python3-dev python3-pip python3-venv python-is-python3 \
    ca-certificates curl wget git \
    ripgrep fd-find jq sqlite3 tree \
    zip unzip xz-utils \
    less nano vim-tiny procps file rsync \
  && ln -s "$(command -v fdfind)" /usr/local/bin/fd \
  && rm -rf /var/lib/apt/lists/*

# GitHub's CLI, for the public side of GitHub (a session has no token, so
# nothing that needs to be logged in).
# renovate: datasource=github-releases depName=cli/cli
ARG GH_VERSION=v2.103.3
RUN set -eux; \
  v="${GH_VERSION#v}"; \
  cd /tmp; \
  curl -fsSLO "https://github.com/cli/cli/releases/download/${GH_VERSION}/gh_${v}_linux_amd64.tar.gz"; \
  curl -fsSLO "https://github.com/cli/cli/releases/download/${GH_VERSION}/gh_${v}_checksums.txt"; \
  grep " gh_${v}_linux_amd64.tar.gz\$" "gh_${v}_checksums.txt" | sha256sum -c -; \
  tar -xzf "gh_${v}_linux_amd64.tar.gz"; \
  install -m 0755 "gh_${v}_linux_amd64/bin/gh" /usr/local/bin/gh; \
  rm -rf /tmp/gh_*

# renovate: datasource=npm depName=pnpm
ARG PNPM_VERSION=12.18.0
RUN npm install -g "pnpm@${PNPM_VERSION}" && npm cache clean --force

COPY --from=go /usr/local/go /usr/local/go
COPY --from=uv /uv /uvx /usr/local/bin/

# SSH into a session (mcloud sandbox ssh): mcloud runs `sshd -i` over the
# exec channel for each connection, as the session's user, so nothing
# listens. The package's host keys go: a session makes its own. (rsync, for
# rsync over SSH, is among the tools above.) The node user's home becomes
# the work volume, which sessions already have as HOME, so SSH logins start
# there. A login shell's /etc/profile sets a PATH of its own: the profile.d
# line gives it back the session's, which mcloud passes in as
# MCLOUD_SSH_PATH.
RUN apt-get update \
  && apt-get install -y --no-install-recommends openssh-server \
  && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_*_key* \
  && usermod -d /work node \
  && echo 'if [ -n "${MCLOUD_SSH_PATH-}" ]; then PATH=$MCLOUD_SSH_PATH; fi' >/etc/profile.d/sandbox-ssh-path.sh

# Last, so a new Claude Code version changes only the layers from here on:
# the node pulls the rest once. No default: an empty version would install
# whatever is latest.
ARG CLAUDE_CODE_VERSION
RUN test -n "$CLAUDE_CODE_VERSION" \
  && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
  && npm cache clean --force

# No updates (the version is the image's), telemetry or error reporting
# from sessions.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

# Sessions run with HOME=/work, and with no write access to /usr/local.
# What a session installs for itself goes under HOME, and its bin directories
# come first on PATH, so that a tool a session installs wins over the
# image's, as on a machine of your own: npm -g (NPM_CONFIG_PREFIX is set in
# users.yaml), pip --user and uv tool, cargo install and rustup, go install,
# bun, deno.
# pip refuses to install into a Debian's Python without
# PIP_BREAK_SYSTEM_PACKAGES (PEP 668); as a user it only ever writes under
# HOME.
ENV PATH=/work/.local/bin:/work/.npm-global/bin:/work/.cargo/bin:/work/go/bin:/work/.bun/bin:/work/.deno/bin:/usr/local/go/bin:$PATH \
    PIP_BREAK_SYSTEM_PACKAGES=1 \
    LANG=C.UTF-8

# The image's own `node` user. Sessions set HOME to their work volume.
USER 1000:1000
WORKDIR /home/node
CMD ["claude", "--version"]

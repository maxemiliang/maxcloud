# The image agent sessions run in (sandbox/sessions/users.yaml): Node,
# git and Claude Code. Built and pushed to ghcr.io/maxemiliang/maxcloud-sandbox
# by .github/workflows/sandbox-image.yaml, which also sets the version.
#
# Claude Code is in the image, at one version, so a session starts with it
# and every session of a tag runs the same one.
FROM node:24-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*

# SSH into a session (mcloud sandbox ssh): mcloud runs `sshd -i` over the
# exec channel for each connection, as the session's user, so nothing
# listens. The package's host keys go: a session makes its own. rsync for
# rsync over SSH. The node user's home becomes the work volume, which
# sessions already have as HOME, so SSH logins start there. A login shell's
# /etc/profile sets a PATH of its own: the profile.d line gives it back the
# session's, which mcloud passes in as MCLOUD_SSH_PATH.
RUN apt-get update \
  && apt-get install -y --no-install-recommends openssh-server rsync \
  && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_*_key* \
  && usermod -d /work node \
  && echo 'if [ -n "${MCLOUD_SSH_PATH-}" ]; then PATH=$MCLOUD_SSH_PATH; fi' >/etc/profile.d/sandbox-ssh-path.sh

# No default: an empty version would install whatever is latest.
ARG CLAUDE_CODE_VERSION
RUN test -n "$CLAUDE_CODE_VERSION" \
  && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
  && npm cache clean --force

# No updates (the version is the image's), telemetry or error reporting
# from sessions.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

# The image's own `node` user. Sessions set HOME to their work volume.
USER 1000:1000
WORKDIR /home/node
CMD ["claude", "--version"]

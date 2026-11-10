# The image agent sessions run in (sandbox/sessions/claude-code.yaml): Node,
# git and Claude Code. Built and pushed to ghcr.io/maxemiliang/maxcloud-sandbox
# by .github/workflows/sandbox-image.yaml, which also sets the version.
#
# Claude Code is in the image so a session needs no package registry: its
# pods may reach the Claude API and nothing else (sandbox/sessions/network.yaml).
FROM node:24-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*

# No default: an empty version would install whatever is latest.
ARG CLAUDE_CODE_VERSION
RUN test -n "$CLAUDE_CODE_VERSION" \
  && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
  && npm cache clean --force

# Updates, telemetry and error reporting all go to hosts a session can't
# reach. Without this Claude Code tries them anyway.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

# The image's own `node` user. Sessions set HOME to their work volume.
USER 1000:1000
WORKDIR /home/node
CMD ["claude", "--version"]

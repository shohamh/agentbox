# Claude Code sandbox image: base + Node + pinned Claude Code, pointed at the
# local Agentbox endpoint (Anthropic Messages API, served by LiteLLM) and with
# every call to Anthropic's own services turned off.
ARG BASE=agentbox-sandbox-base:dev
FROM node:22-bookworm-slim AS node
FROM ${BASE}
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ARG CLAUDE_CODE_VERSION=2.1.322
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && npm cache clean --force
# `claude` is a shim that passes the sandbox token as a bearer token (an API key
# in the environment makes Claude Code ask, at every first start, whether to use
# it); the real binary is claude.real.
COPY claude-shim /usr/local/bin/claude-shim
RUN mv /usr/local/bin/claude /usr/local/bin/claude.real && mv /usr/local/bin/claude-shim /usr/local/bin/claude \
    && chmod 755 /usr/local/bin/claude
COPY settings.json /root/.claude/settings.json
COPY claude.json /root/.claude.json
RUN claude.real --version

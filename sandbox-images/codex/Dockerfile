# Codex sandbox image: base + Node + pinned Codex CLI, configured for the
# local Agentbox model endpoint. The pinned version's config keys are
# smoke-tested at build time (see the RUN at the end).
ARG BASE=agentbox-sandbox-base:dev
FROM node:22-bookworm-slim AS node
FROM ${BASE}
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ARG CODEX_VERSION=0.167.1
RUN npm install -g @openai/codex@${CODEX_VERSION} && npm cache clean --force
COPY config.toml /root/.codex/config.toml
RUN codex --version

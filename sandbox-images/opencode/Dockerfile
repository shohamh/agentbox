# opencode sandbox image: base + Node + pinned opencode, the OpenAI-compatible
# provider preinstalled (no npm mirror needed at runtime), model-catalog fetch
# and update checks off, and a plugin that emits an OTel log per tool call.
ARG BASE=agentbox-sandbox-base:dev
FROM node:22-bookworm-slim AS node
FROM ${BASE}
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ARG OPENCODE_VERSION=1.19.5
ARG AI_SDK_VERSION=3.0.90
RUN npm install -g opencode-ai@${OPENCODE_VERSION} && npm cache clean --force
# opencode resolves provider packages from its config directory's node_modules.
RUN mkdir -p /root/.config/opencode && cd /root/.config/opencode \
    && npm init -y >/dev/null && npm install @ai-sdk/openai-compatible@${AI_SDK_VERSION} && npm cache clean --force
COPY opencode.json /root/.config/opencode/opencode.json
COPY agentbox-otel.js /root/.config/opencode/plugin/agentbox-otel.js
ENV OPENCODE_DISABLE_MODELS_FETCH=true OPENCODE_DISABLE_AUTOUPDATE=true OPENCODE_DISABLE_LSP_DOWNLOAD=true
RUN opencode --version

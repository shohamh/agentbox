# omp (Oh My Pi) sandbox image: base + Node + Bun + pinned omp, with one custom
# provider, "agentbox", for the local Agentbox endpoint (OpenAI-compatible API,
# served by LiteLLM). The model list is written at sandbox start from the
# policy (AGENTBOX_MODELS) by /usr/local/bin/omp-models.
ARG BASE=agentbox-sandbox-base:dev
FROM node:22-bookworm-slim AS node
FROM ${BASE}
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ARG BUN_VERSION=1.4.4
RUN apt-get update && apt-get install -y --no-install-recommends unzip \
    && curl -fsSL -o /tmp/bun.zip "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-x64.zip" \
    && unzip -q /tmp/bun.zip -d /tmp/bun && install -m 755 /tmp/bun/bun-linux-x64/bun /usr/local/bin/bun \
    && rm -rf /tmp/bun /tmp/bun.zip /var/lib/apt/lists/* && bun --version
ARG OMP_VERSION=18.19.0
RUN npm install -g @oh-my-pi/pi-coding-agent@${OMP_VERSION} && npm cache clean --force
COPY omp-models /usr/local/bin/omp-models
COPY omp-shim /usr/local/bin/omp-shim
RUN chmod 755 /usr/local/bin/omp-models /usr/local/bin/omp-shim && mkdir -p /root/.omp/agent \
    && mv /usr/local/bin/omp /usr/local/bin/omp.real && mv /usr/local/bin/omp-shim /usr/local/bin/omp
RUN omp.real --version

# agentbox-console: Go API + the React UI built from web/console.
FROM node:22-bookworm-slim AS ui
WORKDIR /web
COPY web/console/ /web/
RUN if [ -f package.json ]; then \
      (npm ci --no-audit --no-fund || npm install --no-audit --no-fund) && npm run build; \
    else mkdir -p dist && echo '<!doctype html><title>Agentbox</title><p>console UI not built</p>' > dist/index.html; fi

FROM agentbox-base:dev
COPY --from=agentbox-bins:dev /bin/agentbox-console /usr/local/bin/agentbox-console
COPY --from=ui /web/dist /usr/share/agentbox/console
COPY configs/control/console/seed.json /etc/agentbox/seed.json
RUN mkdir -p /etc/agentbox/releases /dynamic
ENV UI_DIR=/usr/share/agentbox/console
ENTRYPOINT ["/usr/local/bin/agentbox-console"]

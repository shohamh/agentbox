# syntax=docker/dockerfile:1
# Playwright runner for the console suite. Build context: the repo root.
#   docker build -f test/ui/Dockerfile -t agentbox-ui-tests .
FROM mcr.microsoft.com/playwright:v1.56.0-noble
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
WORKDIR /ui
COPY test/ui/package.json ./
RUN npm install --no-audit --no-fund
COPY test/ui/ ./
RUN npx tsc --noEmit
CMD ["npx", "playwright", "test"]

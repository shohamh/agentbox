# Small shared base for the Go control-plane services: curl for healthchecks.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl jq procps \
    && rm -rf /var/lib/apt/lists/*

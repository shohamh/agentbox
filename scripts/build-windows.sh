#!/usr/bin/env bash
# Cross-compiles the Windows binaries in a container into dist/windows:
#   agentbox.exe        the CLI
#   agentbox-host.exe   the host service
#   e2e.exe             the E2E suite (go test -c), to run tests against a native PC
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
OUT="dist/windows"; rm -rf "$OUT"; mkdir -p "$OUT"
docker build -q -f - --output "type=local,dest=$OUT" . >/dev/null <<'DOCKERFILE'
# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOOS=windows GOARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/agentbox ./cmd/agentbox-host \
 && go test -c -tags e2e -o /out/e2e.exe ./test/e2e
FROM scratch
COPY --from=build /out/ /
DOCKERFILE
ls -la "$OUT"

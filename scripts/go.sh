#!/usr/bin/env bash
# Runs the Go toolchain inside a BuildKit build (no local Go and no bind
# mounts needed). go.mod/go.sum changes are copied back into the repo.
# Usage: scripts/go.sh mod tidy | scripts/go.sh build ./... | scripts/go.sh test ./pkg/...
set -uo pipefail
cd "$(dirname "$0")/.."
OUT="$(mktemp -d)"
# Docker (a Windows program under Git Bash) needs a Windows path for the output
# directory; this also works when MSYS_NO_PATHCONV=1 turned path conversion off.
OUT_ARG="$OUT"; command -v cygpath >/dev/null 2>&1 && OUT_ARG="$(cygpath -m "$OUT")"
ARGS="$*"
docker build -q -f - --build-arg NONCE="$(date +%s%N)" --build-arg ARGS="$ARGS" --output "type=local,dest=$OUT_ARG" . >/dev/null <<'DOCKERFILE'
# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS run
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false
WORKDIR /src
COPY . .
ARG NONCE
ARG ARGS
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    sh -c "go $ARGS" > /out.log 2>&1; echo $? > /rc; touch go.sum
FROM scratch
COPY --from=run /src/go.mod /src/go.sum /out.log /rc /
DOCKERFILE
rc=$?
if [ $rc -ne 0 ]; then echo "docker build failed ($rc)"; exit $rc; fi
cat "$OUT/out.log"
cp "$OUT/go.mod" go.mod; [ -s "$OUT/go.sum" ] && cp "$OUT/go.sum" go.sum
rc=$(cat "$OUT/rc"); rm -rf "$OUT"; exit "$rc"

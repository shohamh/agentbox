# syntax=docker/dockerfile:1
# End-to-end suite image: the compiled test binary plus the docker CLI.
# Build context: the repo root.   docker build -f test/Dockerfile -t agentbox-tests .
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOFLAGS=-buildvcs=false go test -c -tags e2e -o /e2e.test ./test/e2e

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /e2e.test /e2e.test
ENTRYPOINT ["/e2e.test"]

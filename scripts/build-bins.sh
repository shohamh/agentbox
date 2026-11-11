#!/usr/bin/env bash
# Builds all Go binaries into the image agentbox-bins:dev (no bind mounts).
set -euo pipefail
cd "$(dirname "$0")/.."
docker build -q -f Dockerfile.bins -t agentbox-bins:dev .
echo "built agentbox-bins:dev"

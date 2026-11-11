#!/usr/bin/env bash
# Builds and "releases" the appliance, the simulated PC and the corp-services
# stand-in:
#   agentbox-appliance:prod      the published production image (signed manifest)
#   agentbox-appliance:tampered  prod with one measured binary modified (T1, E2E-10)
#   agentbox-appliance:dev       dev flavor, a different measurement (T3)
#   agentbox-pc:dev, agentbox-corp:dev
# The release manifest (PCR 11 + every measured file hash) is produced inside
# the prod image, signed with the release key and written into the volume the
# console reads as RELEASES_DIR. Only prod is published.
#
# Inputs (named volumes written by the control plane's bootstrap; override as needed):
#   SECRETS_VOLUME   volume with policy.pub and release.key        (default agentbox_secrets)
#   RELEASES_VOLUME  volume mounted at the console's RELEASES_DIR  (default agentbox_releases)
#   POLICY_PUB / RELEASE_KEY   alternatively, paths to key files (copied into a temp volume)
#   VERSION          release version (default 1.0.0-poc)
#   SKIP_BINS=1      reuse the existing agentbox-bins:dev image
#   WITH_DEV_MANIFEST=1  also publish a signed dev manifest so the verifier can say "dev flavor"
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-1.0.0-poc}"
SECRETS_VOLUME="${SECRETS_VOLUME:-agentbox_secrets}"
RELEASES_VOLUME="${RELEASES_VOLUME:-agentbox_releases}"
export MSYS_NO_PATHCONV=1

if [ -n "${POLICY_PUB:-}" ] || [ -n "${RELEASE_KEY:-}" ]; then
  SECRETS_VOLUME=agentbox_release_tmp
  docker volume rm -f "$SECRETS_VOLUME" >/dev/null 2>&1 || true
  docker volume create "$SECRETS_VOLUME" >/dev/null
  docker rm -f abx-sec-tmp >/dev/null 2>&1 || true
  docker create --name abx-sec-tmp -v "$SECRETS_VOLUME:/s" debian:bookworm-slim true >/dev/null
  if [ -n "${POLICY_PUB:-}" ]; then docker cp "$POLICY_PUB" abx-sec-tmp:/s/policy.pub; fi
  if [ -n "${RELEASE_KEY:-}" ]; then docker cp "$RELEASE_KEY" abx-sec-tmp:/s/release.key; fi
  docker rm abx-sec-tmp >/dev/null
fi
docker volume create "$RELEASES_VOLUME" >/dev/null

if [ "${SKIP_BINS:-}" != 1 ]; then scripts/build-bins.sh; fi
mkdir -p image/.build
docker run --rm -v "$SECRETS_VOLUME:/s:ro" debian:bookworm-slim cat /s/policy.pub > image/.build/policy.pub
[ -s image/.build/policy.pub ] || { echo "policy.pub missing in volume $SECRETS_VOLUME"; exit 1; }

echo "== appliance images"
docker build -q -f image/Dockerfile.appliance --build-arg FLAVOR=prod --build-arg VERSION="$VERSION" -t agentbox-appliance:prod .
docker build -q -f image/Dockerfile.appliance --build-arg FLAVOR=dev --build-arg VERSION="$VERSION" -t agentbox-appliance:dev .
docker build -q -f image/Dockerfile.tampered -t agentbox-appliance:tampered .

sign() { # $1 = image, $2 = output file name
  docker run --rm --entrypoint agentbox-measure "$1" manifest \
    | docker run --rm -i -v "$SECRETS_VOLUME:/s:ro" -v "$RELEASES_VOLUME:/r" --entrypoint /bin/policy-sign agentbox-bins:dev manifest /s/release.key "/r/$2"
}
echo "== release manifest"
sign agentbox-appliance:prod "prod-$VERSION.json"
if [ "${WITH_DEV_MANIFEST:-}" = 1 ]; then sign agentbox-appliance:dev "dev-$VERSION.json"; fi

echo "== pc and corp-services"
docker build -q -f image/Dockerfile.pc -t agentbox-pc:dev .
docker build -q -f image/Dockerfile.corp -t agentbox-corp:dev .
echo "released $VERSION: prod (signed), dev, tampered, pc, corp"

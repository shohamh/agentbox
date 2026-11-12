#!/usr/bin/env bash
# Builds the bootable appliance disk (VHDX) and its signed release manifest.
#   scripts/build-vm.sh            -> dist/vm/agentbox-appliance.vhdx (+ manifest in the releases volume)
#   FLAVOR=dev scripts/build-vm.sh -> a dev image (serial console login); its measurement differs
#   TEST=1 scripts/build-vm.sh     -> dist/vm/agentbox-appliance-test.vhdx for the QEMU boot test
# Needs agentbox-bins:dev and agentbox-appliance:prod (scripts/build-bins.sh, scripts/release.sh)
# and the secrets volume from scripts/bootstrap.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
FLAVOR="${FLAVOR:-prod}"
VERSION="${VERSION:-1.0.0-poc-vm}"
SECRETS_VOLUME="${SECRETS_VOLUME:-agentbox_secrets}"
RELEASES_VOLUME="${RELEASES_VOLUME:-agentbox_releases}"
# Not measured (the boot partition): show the appliance log on the VM console so a Hyper-V
# console window (vmconnect) tells you what it is doing.
EXTRA="systemd.journald.forward_to_console=1 systemd.journald.max_level_console=info"; NAME="agentbox-appliance"
if [ "${TEST:-0}" = 1 ]; then EXTRA="agentbox.control=tcp://0.0.0.0:7000 agentbox.no_site=1 systemd.journald.forward_to_console=1 systemd.journald.max_level_console=info"; NAME="agentbox-appliance-test"; fi
mkdir -p image/.build dist/vm
docker run --rm -v "$SECRETS_VOLUME:/s:ro" debian:bookworm-slim cat /s/policy.pub > image/.build/policy.pub
[ -s image/.build/policy.pub ] || { echo "policy.pub missing in volume $SECRETS_VOLUME (run scripts/bootstrap.sh)"; exit 1; }
docker image inspect agentbox-appliance:prod >/dev/null 2>&1 || { echo "build agentbox-appliance:prod first (scripts/release.sh)"; exit 1; }
OUT="dist/vm/.out"; rm -rf "$OUT"; mkdir -p "$OUT"
docker build -f image/vm/Dockerfile.vm --target out --build-arg FLAVOR="$FLAVOR" --build-arg VERSION="$VERSION" \
  --build-arg EXTRA="$EXTRA" --output "type=local,dest=$OUT" .
mv "$OUT/disk.vhdx" "dist/vm/$NAME.vhdx"
cp "$OUT/manifest.unsigned.json" "dist/vm/$NAME.manifest.unsigned.json"
# Sign the manifest with the release key and publish it to the console (prod and the test image share a rootfs).
if [ "${TEST:-0}" != 1 ]; then
  docker volume create "$RELEASES_VOLUME" >/dev/null
  docker run --rm -i -v "$SECRETS_VOLUME:/s:ro" -v "$RELEASES_VOLUME:/r" --entrypoint /bin/policy-sign agentbox-bins:dev \
    manifest /s/release.key "/r/vm-$FLAVOR-$VERSION.json" < "dist/vm/$NAME.manifest.unsigned.json"
fi
rm -rf "$OUT"
ls -la "dist/vm/"

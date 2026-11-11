#!/usr/bin/env bash
# Boots dist/vm/agentbox-appliance-test.vhdx under QEMU (UEFI Secure Boot,
# software TPM) and checks the appliance came up: Secure Boot on, the TPM device
# works, PCR 11 equals the release manifest, the manager answers.
set -uo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
DISK="${DISK:-dist/vm/agentbox-appliance-test.vhdx}"
MANIFEST="${MANIFEST:-dist/vm/agentbox-appliance-test.manifest.unsigned.json}"
[ -f "$DISK" ] || { echo "build it first: TEST=1 scripts/build-vm.sh"; exit 2; }
docker build -q -f image/vm/Dockerfile.qemu -t agentbox-qemu . >/dev/null
docker rm -f abx-vmtest >/dev/null 2>&1
docker create --name abx-vmtest agentbox-qemu >/dev/null
trap 'docker rm -f abx-vmtest >/dev/null 2>&1' EXIT
docker cp "$DISK" abx-vmtest:/vm/disk.vhdx
docker start abx-vmtest >/dev/null
echo "booting (TCG, no KVM: this takes a few minutes)..."
END=$(( $(date +%s) + ${BOOT_TIMEOUT:-900} ))
OUT=""
while [ "$(date +%s)" -lt "$END" ]; do
  OUT=$(docker exec abx-vmtest curl -s -m 5 http://127.0.0.1:7000/v1/diagnostics 2>/dev/null || true)
  [ -n "$OUT" ] && break
  docker ps -q --filter name=abx-vmtest | grep -q . || { echo "QEMU exited"; break; }
  sleep 10
done
if [ -z "$OUT" ]; then
  echo "FAIL: the appliance did not answer"; echo "--- serial console (tail)"; docker exec abx-vmtest tail -60 /vm/serial.log 2>&1; exit 1
fi
J() { printf '%s' "$OUT" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -1; }
WANT=$(sed -n 's/.*"pcr11":"\([0-9a-f]*\)".*/\1/p' "$MANIFEST" | head -1)
GOT=$(J pcr11); SB=$(J secure_boot)
echo "secure boot : $SB"; echo "pcr11 (vm)  : $GOT"; echo "pcr11 (rel) : $WANT"
echo "pcr11 error : $(J pcr11_error)"; echo "tpm path    : $(J tpm_path)"; echo "kernel      : $(printf '%s' "$OUT" | sed -n 's/.*"kernel":"\([^"]*\)".*/\1/p' | cut -c1-70)"
echo "services    : $(J services)"; echo "data disk   : $(J data_disk)"
rc=0
[ "$SB" = on ] || { echo "FAIL: Secure Boot is not on"; rc=1; }
[ -n "$GOT" ] && [ "$GOT" = "$WANT" ] || { echo "FAIL: PCR 11 does not match the release manifest"; rc=1; }
[ $rc = 0 ] && echo "PASS: the appliance image boots under Secure Boot and measures to the release manifest"
exit $rc

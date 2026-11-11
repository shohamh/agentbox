#!/bin/bash
# Appliance boot: virtual TPM, measured boot into PCR 11, then agentbox-manager.
# The container runtime restarts this container when the manager exits, which
# is how a recycle "reboots" the appliance from its read-only base.
set -euo pipefail
mkdir -p /run/swtpm /run/agentbox/certs /data/vtpm /data/otelq /var/log/agentbox /run/netns
rm -f /run/swtpm/*.sock /run/agentbox/eventlog.json

if [ ! -e /data/vtpm/tpm2-00.permall ]; then
  swtpm_setup --tpm2 --tpmstate /data/vtpm --createek --overwrite --config /etc/swtpm_setup.conf >/dev/null
fi
swtpm socket --tpm2 --tpmstate dir=/data/vtpm \
  --server type=unixio,path=/run/swtpm/tpm.sock \
  --ctrl type=unixio,path=/run/swtpm/ctrl.sock \
  --flags not-need-init,startup-clear >/var/log/agentbox/swtpm.log 2>&1 &

for _ in $(seq 1 50); do [ -S /run/swtpm/tpm.sock ] && break; sleep 0.2; done
agentbox-measure boot -tpm /run/swtpm/tpm.sock -log /run/agentbox/eventlog.json

# The private PC<->appliance network stands in for vsock.
if [ -n "${AGENTBOX_CONTROL_LISTEN:-}" ]; then export AGENTBOX_CONTROL_LISTEN; fi
exec agentbox-manager /etc/agentbox/appliance.json

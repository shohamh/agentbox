#!/bin/bash
# Simulated PC start-up. Environment:
#   DEVICE_ID          this PC's device ID (must be in the Fleet inventory)
#   APPLIANCE_ADDR     host:port of the paired appliance on the private network
#   OIDC_ISSUER        default http://kc.agentbox.corp:8080/realms/agentbox
#   ENROLL_URL         default https://enroll.agentbox.corp:8443
#   CA_URL             default https://ca.agentbox.corp:9000/roots.pem
#   CA_FINGERPRINT     sha256 of the step-ca root certificate (hex). The real
#                      installer ships the root; here it is fetched and pinned.
#                      If unset the root is trusted on first use, with a warning.
# A root already at /etc/agentbox/ca.crt (docker cp) is used as is.
set -euo pipefail
: "${DEVICE_ID:?DEVICE_ID is required}"
: "${APPLIANCE_ADDR:?APPLIANCE_ADDR is required}"
CA_URL="${CA_URL:-https://ca.agentbox.corp:9000/roots.pem}"

if [ ! -s /etc/agentbox/ca.crt ]; then
  echo "pc: fetching the corporate root CA from $CA_URL"
  ok=0
  for _ in $(seq 1 90); do
    if curl -sk --max-time 5 "$CA_URL" -o /tmp/roots.pem && grep -q BEGIN /tmp/roots.pem; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = 1 ] || { echo "pc: could not fetch the root CA"; exit 1; }
  fp=$(openssl x509 -in /tmp/roots.pem -outform DER | sha256sum | cut -d' ' -f1)
  if [ -n "${CA_FINGERPRINT:-}" ] && [ "$fp" != "${CA_FINGERPRINT,,}" ]; then
    echo "pc: root CA fingerprint $fp does not match the pinned $CA_FINGERPRINT"; exit 1
  fi
  [ -n "${CA_FINGERPRINT:-}" ] || echo "pc: WARNING no CA_FINGERPRINT pinned; trusting $fp on first use"
  install -m644 /tmp/roots.pem /etc/agentbox/ca.crt
fi

cat > /etc/agentbox/host.json <<EOF
{
  "driver": "container",
  "appliance_addr": "tcp://${APPLIANCE_ADDR}",
  "state_dir": "/var/lib/agentbox",
  "socket": "/run/agentbox/host.sock",
  "enroll_url": "${ENROLL_URL:-https://enroll.agentbox.corp:8443}",
  "oidc_issuer": "${OIDC_ISSUER:-http://kc.agentbox.corp:8080/realms/agentbox}",
  "oidc_client_id": "agentbox-cli",
  "ca_root_file": "/etc/agentbox/ca.crt",
  "device_id": "${DEVICE_ID}",
  "allow_password_grant": true
}
EOF
exec agentbox-host /etc/agentbox/host.json

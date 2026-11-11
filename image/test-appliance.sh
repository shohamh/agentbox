#!/usr/bin/env bash
# Standalone appliance test (no control plane). Builds a throwaway world:
# keys, a registry holding the base sandbox image, a corp-services stand-in and
# one appliance whose state.json carries a locally signed bundle. Then checks:
#   measured boot: PCR 11 in the TPM equals the signed release manifest
#   the control channel answers, the AK/EK are stable
#   a gVisor sandbox starts from the pinned image, sshd works over the tunnel
#   egress: allowed host reachable, other host refused, DNS refuses unknown names
#   the model endpoint refuses an unattested appliance with a readable error
# Everything is prefixed abxt- and removed at the end (KEEP=1 keeps it).
set -uo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
N=abxt
pass=0; failn=0
ok()   { echo "  PASS $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; failn=$((failn+1)); }
check() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
cleanup() {
  [ "${KEEP:-}" = 1 ] && return
  docker rm -f $N-appliance $N-registry $N-corp >/dev/null 2>&1
  docker network rm $N-net >/dev/null 2>&1
  docker volume rm -f $N-secrets $N-releases $N-data >/dev/null 2>&1
}
cleanup
trap cleanup EXIT

docker network create $N-net >/dev/null
docker volume create $N-secrets >/dev/null; docker volume create $N-data >/dev/null
docker run --rm -v $N-secrets:/s --entrypoint /bin/policy-sign agentbox-bins:dev keygen /s >/dev/null
SECRETS_VOLUME=$N-secrets RELEASES_VOLUME=$N-releases SKIP_BINS=1 scripts/release.sh | tail -3

echo "== registry with the base sandbox image"
docker run -d --name $N-registry --network $N-net --network-alias registry.agentbox.corp -p 15100:5000 registry:2 >/dev/null
docker build -q -t agentbox-sandbox-base:dev sandbox-images/base >/dev/null
docker tag agentbox-sandbox-base:dev localhost:15100/agentbox/base:dev
for _ in $(seq 1 20); do docker push -q localhost:15100/agentbox/base:dev >/dev/null 2>&1 && break; sleep 1; done
DIGEST=$(docker inspect --format '{{index .RepoDigests 0}}' localhost:15100/agentbox/base:dev | sed 's/.*@//')
echo "  base digest $DIGEST"

echo "== corp-services"
docker run -d --name $N-corp --network $N-net --network-alias git.agentbox.corp --network-alias blocked.agentbox.corp agentbox-corp:dev >/dev/null

echo "== appliance with a pre-signed bundle"
docker build -q -f image/Dockerfile.mkbundle -t agentbox-mkbundle:test . >/dev/null
docker run --rm -v $N-secrets:/s:ro --entrypoint /mkbundle agentbox-mkbundle:test -key /s/policy.key -device pc-test -user alice \
  -egress git.agentbox.corp -image "base=registry.agentbox.corp:5000/agentbox/base@$DIGEST" 2>/dev/null \
  | docker run --rm -i -v $N-data:/data debian:bookworm-slim sh -c 'cat > /data/state.json'
docker run -d --name $N-appliance --privileged --network $N-net agentbox-appliance:prod >/dev/null
for _ in $(seq 1 40); do docker exec $N-appliance curl -sf http://127.0.0.1:7000/v1/status >/dev/null 2>&1 && break; sleep 1; done
A() { docker exec $N-appliance "$@"; }

echo "== measured boot"
BOOTMSG=$(docker logs $N-appliance 2>&1 | grep "measured .* entries into PCR 11" | tail -1)
echo "  $BOOTMSG"
PCR=$(echo "$BOOTMSG" | sed 's/.*PCR 11 = \([0-9a-f]*\).*/\1/')
MANIFEST_PCR=$(docker run --rm -v $N-releases:/r:ro debian:bookworm-slim cat /r/prod-1.0.0-poc.json | grep -o '"pcr11":"[0-9a-f]*"' | cut -d'"' -f4)
[ -n "$PCR" ] && [ "$PCR" = "$MANIFEST_PCR" ] && ok "PCR 11 equals the signed release manifest" || bad "PCR mismatch: tpm=$PCR manifest=$MANIFEST_PCR"

echo "== control channel"
ID1=$(A curl -sf http://127.0.0.1:7000/v1/identity | head -c 300); [ -n "$ID1" ] && ok "identity answers" || bad "identity"
A curl -sf http://127.0.0.1:7000/v1/status | grep -q '"attestation"' && ok "status answers" || bad "status"
A pgrep -x envoy >/dev/null && ok "envoy running" || bad "envoy running"
A pgrep -f otelcol-contrib >/dev/null && ok "otel collector running" || bad "otel collector running"

echo "== sandbox"
KEY=$(docker run --rm debian:bookworm-slim sh -c 'ls /etc >/dev/null; echo ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDummyKeyDummyKeyDummyKeyDummyKeyDummyKey1 test')
SB=$(A curl -s -X POST http://127.0.0.1:7000/v1/sandboxes -d "{\"name\":\"t1\",\"image\":\"base\",\"ssh_pub\":\"$KEY\"}")
echo "  $SB" | head -c 300; echo
echo "$SB" | grep -q '"state":"running"' && ok "sandbox running under gVisor" || bad "sandbox start"
sleep 4
SBID=$(echo "$SB" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
SIP=$(echo "$SB" | grep -o '"ip":"[^"]*"' | head -1 | cut -d'"' -f4)
A sh -c "echo | nc -w 3 $SIP 22" 2>/dev/null | grep -q SSH-2.0 && ok "sshd answers inside the sandbox" || bad "sshd banner"
X() { A runsc --root /run/runsc exec "$SBID" "$@"; }
X /bin/bash -lc 'cd /workspace && ls' 2>&1 | grep -q calc.py && ok "workspace populated from the sample repo" || bad "workspace"
X /bin/bash -lc 'cd /workspace && python3 -m unittest discover 2>&1 | tail -1' 2>&1 | grep -q OK && ok "sample tests pass inside the sandbox" || bad "sample tests"
X /bin/bash -c 'env' 2>&1 | grep -q '^AGENTBOX_TOKEN=abx-sbx-' && ok "sandbox environment carries the local token" || bad "sandbox env"
X /bin/bash -c 'env | grep -ci "litellm\|sk-"' 2>&1 | grep -q '^0' && ok "no real credential in the sandbox environment" || bad "credential grep"
X /bin/bash -c 'curl -s -m 5 -x http://10.88.0.1:3128 http://git.agentbox.corp/x' 2>&1 | grep -q '"service": "git.agentbox.corp"' && ok "allowed egress host reachable through the proxy" || bad "allowed egress"
X /bin/bash -c 'curl -s -m 5 -x http://10.88.0.1:3128 http://blocked.agentbox.corp/x' 2>&1 | grep -q "egress to blocked.agentbox.corp is blocked" && ok "other host refused with a helpful message" || bad "blocked egress"
X /bin/bash -c 'getent hosts blocked.agentbox.corp || echo NXDOMAIN' 2>&1 | grep -q NXDOMAIN && ok "DNS answers NXDOMAIN for names outside the allowlist" || bad "dns allowlist"
X /bin/bash -c 'curl --noproxy "*" -s -m 5 http://git.agentbox.corp/x -o /dev/null; echo rc=$?' 2>&1 | grep -Eq "rc=[1-9]" && ok "direct (unproxied) traffic to the internal network is dropped" || bad "direct egress"
X /bin/bash -c 'curl -s -m 8 http://llm.agentbox:4000/v1/models -H "Authorization: Bearer $AGENTBOX_TOKEN"' 2>&1 | grep -q "attestation" && ok "model endpoint refuses an unattested appliance with a clear message" || bad "model endpoint authz"
X /bin/bash -c 'curl -s -m 8 http://llm.agentbox:4000/v1/models -H "Authorization: Bearer abx-sbx-wrong"' 2>&1 | grep -q "token" && ok "wrong token refused" || bad "wrong token"
A curl -sf -X POST http://127.0.0.1:7000/v1/sandboxes/$SBID/stop >/dev/null && ok "sandbox stops" || bad "sandbox stop"

echo "== recycle keeps state and AK"
A curl -sf -X POST http://127.0.0.1:7000/v1/recycle >/dev/null
sleep 3; docker start $N-appliance >/dev/null 2>&1
for _ in $(seq 1 40); do A curl -sf http://127.0.0.1:7000/v1/status >/dev/null 2>&1 && break; sleep 1; done
ID2=$(A curl -sf http://127.0.0.1:7000/v1/identity | head -c 300)
[ -n "$ID2" ] && [ "$ID1" = "$ID2" ] && ok "AK and EK identical after reboot" || bad "AK changed across reboot"
A curl -s http://127.0.0.1:7000/v1/sandboxes | grep -q '"name":"t1"' && ok "sandbox record survived the reboot" || bad "sandbox record"

echo; echo "passed $pass, failed $failn"
[ "$failn" = 0 ]

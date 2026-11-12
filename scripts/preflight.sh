#!/usr/bin/env bash
# Checks a LiteLLM proxy before Agentbox's gateway is pointed at it, and after
# every LiteLLM upgrade (docs/litellm-admin.md). Runs agentbox-preflight from the
# agentbox-bins image; nothing is installed.
#
#   scripts/preflight.sh --url https://litellm.corp.example \
#       --master-key-file ./litellm_master_key --models claude-sonnet,claude-haiku
#
#   CA_FILE=corp-ca.pem       trust this CA bundle (a LiteLLM behind your own CA)
#   DOCKER_NETWORK=agentbox_agentbox   run on that network (the test bed's LiteLLM is http://litellm:4000)
#   LITELLM_MASTER_KEY=...    instead of --master-key-file
# Other flags: --ttl 15m  --no-completions  --no-expiry
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
docker image inspect agentbox-bins:dev >/dev/null 2>&1 || scripts/build-bins.sh

args=()
run=(docker run --rm)
[ -n "${DOCKER_NETWORK:-}" ] && run+=(--network "$DOCKER_NETWORK")
[ -n "${LITELLM_MASTER_KEY:-}" ] && run+=(-e LITELLM_MASTER_KEY)
if [ -n "${CA_FILE:-}" ]; then
  run+=(-v "$(realpath "$CA_FILE"):/ca.pem:ro" -e SSL_CERT_FILE=/ca.pem)
fi
while [ $# -gt 0 ]; do
  case "$1" in
    --master-key-file) run+=(-v "$(realpath "$2"):/run/master.key:ro"); args+=(--master-key-file /run/master.key); shift 2;;
    --master-key-file=*) f="${1#*=}"; run+=(-v "$(realpath "$f"):/run/master.key:ro"); args+=(--master-key-file /run/master.key); shift;;
    *) args+=("$1"); shift;;
  esac
done
exec "${run[@]}" --entrypoint /bin/agentbox-preflight agentbox-bins:dev "${args[@]}"

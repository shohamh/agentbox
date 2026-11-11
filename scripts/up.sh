#!/usr/bin/env bash
# Builds and starts the control plane, waiting until every service is healthy.
#   scripts/up.sh            control plane
#   scripts/up.sh grafana    ... plus the Grafana dashboards (extra ~150 MB RAM)
#   scripts/up.sh down       stop it (volumes kept); scripts/up.sh nuke removes volumes too
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
DC="docker compose -f deploy/compose/docker-compose.yml --env-file deploy/compose/.secrets/compose.env"

case "${1:-}" in
  down) $DC --profile grafana down; exit 0;;
  nuke) $DC --profile grafana down -v; docker volume rm agentbox_secrets 2>/dev/null || true
        rm -rf deploy/compose/.secrets; exit 0;;
esac

scripts/build-bins.sh
scripts/bootstrap.sh
PROFILE=()
[ "${1:-}" = grafana ] && PROFILE=(--profile grafana)
$DC "${PROFILE[@]}" build
$DC "${PROFILE[@]}" up -d --wait --wait-timeout 600
$DC "${PROFILE[@]}" ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}\t{{.Ports}}'
echo
echo "console:   http://console.agentbox.corp:18080  (127.0.0.1:18080)   users bob/erin (admin) carol (approver) frank (viewer), password Passw0rd!"
echo "keycloak:  http://kc.agentbox.corp:8080/realms/agentbox  (127.0.0.1:18180)"
echo "registry:  registry.agentbox.corp:5000  (127.0.0.1:15000)"

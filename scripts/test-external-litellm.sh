#!/usr/bin/env bash
# Proves the "company LiteLLM" deployment (docs/litellm-admin.md): starts a
# second LiteLLM that plays the company's proxy, stops the bundled one, points the
# gateway, the front Envoy and the reviewer at it with
# deploy/compose/external-litellm.yml, and runs the tests that depend on LiteLLM
# (Codex task, Claude Code task, short-lived keys, the readiness check) against it.
# Twice: over plain HTTP, and over HTTPS from a private CA behind a proxy that
# only answers for its own host name (the shape of a real company LiteLLM).
# Then puts the bundled LiteLLM back.
#
# Needs the test bed running (see docs/testing.md) with the appliance overlay up.
#   MODES="http https" scripts/test-external-litellm.sh    (default both)
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
export AGENTBOX_KEY_TTL="${AGENTBOX_KEY_TTL:-90s}"
ENVF=deploy/compose/.secrets/compose.env
BASE=(-p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml --env-file "$ENVF")
PW="$(grep '^POSTGRES_PASSWORD=' "$ENVF" | cut -d= -f2)"
COMPANY=company-litellm
PROXY=company-llm-proxy
TLSVOL=abx_ext_tls
TMPD="$(mktemp -d)"
TMPW="$TMPD"; command -v cygpath >/dev/null 2>&1 && TMPW="$(cygpath -m "$TMPD")"   # a path docker compose (a Windows program) can open

restore() {
  echo "== restoring the bundled LiteLLM"
  docker rm -f "$COMPANY" "$PROXY" >/dev/null 2>&1 || true
  docker volume rm "$TLSVOL" >/dev/null 2>&1 || true
  docker start agentbox-litellm-1 >/dev/null 2>&1 || true
  unset LITELLM_URL LITELLM_CA_FILE REVIEWER_MODEL
  docker compose "${BASE[@]}" up -d --wait gatekeeper front-envoy reviewer console 2>&1 | grep -v "was not created" | tail -3 || true
  rm -rf "$TMPD"
}
trap restore EXIT

echo "== starting the company LiteLLM ($COMPANY): same models, its own container"
docker rm -f "$COMPANY" "$PROXY" >/dev/null 2>&1 || true
docker run -d --name "$COMPANY" --network agentbox \
  -e "DATABASE_URL=postgresql://agentbox:${PW}@postgres:5432/litellm" -e OTLP_ENDPOINT=http://otel-gateway:4319/v1/logs \
  -v agentbox_secrets:/secrets:ro agentbox/litellm:dev >/dev/null
for i in $(seq 1 60); do
  docker exec "$COMPANY" python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:4000/health/liveliness', timeout=3)" >/dev/null 2>&1 && break
  [ "$i" = 60 ] && { echo "the company LiteLLM did not start"; docker logs "$COMPANY" | tail -20; exit 1; }
  sleep 3
done
docker stop agentbox-litellm-1 >/dev/null

# A private CA, a certificate for llm.corp.example, and an nginx that answers
# only for that host name (a request with another Host header gets 404).
setup_tls() {
  docker volume rm "$TLSVOL" >/dev/null 2>&1 || true
  docker run --rm -v "$TLSVOL:/t" agentbox-base:dev sh -ec '
    cd /t
    openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.pem -days 2 -subj "/CN=Company test CA" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -keyout srv.key -out srv.csr -subj "/CN=llm.corp.example" 2>/dev/null
    printf "subjectAltName=DNS:llm.corp.example" > ext.cnf
    openssl x509 -req -in srv.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out srv.pem -days 2 -extfile ext.cnf 2>/dev/null
    cat > default.conf <<NGINX
server {
  listen 443 ssl;
  server_name llm.corp.example;
  ssl_certificate /t/srv.pem;
  ssl_certificate_key /t/srv.key;
  location / {
    if (\$host != "llm.corp.example") { return 404; }
    proxy_pass http://company-litellm:4000;
    proxy_set_header Host \$host;
    proxy_buffering off;
    proxy_read_timeout 300s;
  }
}
NGINX
    chmod -R a+r /t'
  docker run -d --name "$PROXY" --network agentbox --network-alias llm.corp.example -v "$TLSVOL:/t:ro" \
    -v "$TLSVOL:/etc/nginx/conf.d:ro" nginx:1.27-alpine >/dev/null
  sleep 3
  docker logs "$PROXY" 2>&1 | grep -qi "emerg" && { docker logs "$PROXY" | tail; exit 1; } || true
}

run_mode() {
  local mode="$1"
  echo; echo "================ $mode ================"
  local ov="$TMPD/ext-$mode.yml" ovw="$TMPW/ext-$mode.yml"
  if [ "$mode" = https ]; then
    setup_tls
    export LITELLM_URL="https://llm.corp.example" LITELLM_CA_FILE=/ext-tls/ca.pem
    cat > "$ov" <<'YML'
# CA bundle for the three services that talk to the company LiteLLM over TLS.
services:
  gatekeeper:
    environment: {SSL_CERT_FILE: /ext-tls/ca.pem}
    volumes: [abx_ext_tls:/ext-tls:ro]
  reviewer:
    environment: {SSL_CERT_FILE: /ext-tls/ca.pem}
    volumes: [abx_ext_tls:/ext-tls:ro]
  front-envoy:
    volumes: [abx_ext_tls:/ext-tls:ro]
volumes:
  abx_ext_tls: {external: true}
YML
  else
    export LITELLM_URL="http://${COMPANY}:4000"; unset LITELLM_CA_FILE
    printf 'services: {}\n' > "$ov"
  fi
  export REVIEWER_MODEL=glm-4
  local EXT=(-p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml -f deploy/compose/external-litellm.yml -f "$ovw" --env-file "$ENVF")
  docker compose "${EXT[@]}" up -d --build --wait gatekeeper front-envoy 2>&1 | grep -v "was not created" | tail -3
  docker compose "${EXT[@]}" up -d litellm-init reviewer console 2>&1 | grep -v "was not created" | tail -3
  sleep 5
  [ "$(docker inspect -f '{{.State.Running}}' agentbox-litellm-1)" = false ] || { echo "the bundled LiteLLM is running: the test would prove nothing"; exit 1; }
  docker inspect agentbox-gatekeeper-1 -f '{{range .Config.Env}}{{println .}}{{end}}' | grep -q "^LITELLM_URL=${LITELLM_URL}$" || { echo "the gateway does not use $LITELLM_URL"; exit 1; }
  docker logs agentbox-front-envoy-1 2>&1 | grep "front-envoy: model requests go to" | tail -1
  echo "== tests against the company LiteLLM ($mode)"
  STACK_UP=1 SKIP_BUILD=1 TEST=E2E-03,E2E-16,E2E-18,E2E-20 bash scripts/test.sh
}

rc=0
for m in ${MODES:-http https}; do run_mode "$m" || rc=1; done
exit $rc

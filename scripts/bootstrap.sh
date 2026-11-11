#!/usr/bin/env bash
# Generates the control plane's secrets, idempotently, without bind mounts:
#   named volume agentbox_secrets   policy.key/.pub, release.key/.pub, stepca_password,
#                                   litellm_master_key, reviewer_key (files, mode 0644: test bed)
#   deploy/compose/.secrets/compose.env   passwords Compose interpolates (never committed)
# Also builds agentbox-bins:dev and agentbox-base:dev if missing.
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
SECRETS_VOL=agentbox_secrets
ENVDIR=deploy/compose/.secrets
ENVFILE=$ENVDIR/compose.env

have() { docker image inspect "$1" >/dev/null 2>&1; }
have agentbox-bins:dev || scripts/build-bins.sh
have agentbox-base:dev || docker build -q -f deploy/compose/base.Dockerfile -t agentbox-base:dev deploy/compose >/dev/null

docker volume inspect $SECRETS_VOL >/dev/null 2>&1 || docker volume create $SECRETS_VOL >/dev/null

# Signing keys (Ed25519): policy (online signing key) and release (manifest signing).
docker run --rm -v $SECRETS_VOL:/s --entrypoint /bin/policy-sign agentbox-bins:dev keygen /s

# Random secrets generated inside a container; existing ones are kept.
docker run --rm -v $SECRETS_VOL:/s agentbox-base:dev sh -ec '
  rnd() { head -c 48 /dev/urandom | base64 | tr -dc "A-Za-z0-9" | head -c "$1"; }
  [ -s /s/stepca_password ]     || rnd 32 > /s/stepca_password
  [ -s /s/litellm_master_key ]  || echo "sk-abx-master-$(rnd 32)" > /s/litellm_master_key
  [ -s /s/reviewer_key ]        || echo "sk-abx-reviewer-$(rnd 32)" > /s/reviewer_key
  chmod 0644 /s/*
  ls /s'

mkdir -p "$ENVDIR"
if [ ! -s "$ENVFILE" ]; then
  rnd() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1" || true; }
  {
    echo "POSTGRES_PASSWORD=$(rnd 24)"
    echo "CLICKHOUSE_PASSWORD=$(rnd 24)"
    echo "OIDC_CLIENT_SECRET=$(rnd 32)"
    echo "KC_ADMIN_PASSWORD=$(rnd 20)"
  } > "$ENVFILE"
  echo "wrote $ENVFILE"
fi
echo "bootstrap complete"

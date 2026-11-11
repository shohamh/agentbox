#!/usr/bin/env bash
# Builds the three sandbox images, pushes them to the internal registry and
# publishes the digest catalog to the console.
#   REGISTRY_PUSH   where this machine reaches the registry   (default localhost:15000)
#   REGISTRY_REF    the name appliances pull from             (default registry.agentbox.corp:5000)
#   CONSOLE         console container name                    (default agentbox-console-1)
# Writes images.json (name -> {ref, digest, harness}) and copies it to the
# console's /etc/agentbox/images.json (the seed file's images_file).
set -euo pipefail
cd "$(dirname "$0")/.."
PUSH="${REGISTRY_PUSH:-localhost:15000}"
REF="${REGISTRY_REF:-registry.agentbox.corp:5000}"
CONSOLE="${CONSOLE:-agentbox-console-1}"
OUT="${IMAGES_JSON:-image/.build/images.json}"
mkdir -p "$(dirname "$OUT")"

docker build -q -t agentbox-sandbox-base:dev sandbox-images/base >/dev/null
docker build -q -t agentbox-sandbox-codex:dev --build-arg BASE=agentbox-sandbox-base:dev sandbox-images/codex >/dev/null
docker build -q -t agentbox-sandbox-opencode:dev --build-arg BASE=agentbox-sandbox-base:dev sandbox-images/opencode >/dev/null

json="{"
sep=""
for name in base codex opencode; do
  harness=""; [ "$name" = codex ] && harness=codex; [ "$name" = opencode ] && harness=opencode
  docker tag "agentbox-sandbox-$name:dev" "$PUSH/agentbox/$name:dev"
  for attempt in 1 2 3 4 5; do
    docker push -q "$PUSH/agentbox/$name:dev" >/dev/null && break
    [ "$attempt" = 5 ] && { echo "push of $name failed after $attempt attempts" >&2; exit 1; }
    echo "push of $name failed (attempt $attempt), retrying..." >&2; sleep $((attempt * 5))
  done
  digest=$(docker inspect --format '{{index .RepoDigests 0}}' "$PUSH/agentbox/$name:dev" | sed 's/.*@//')
  echo "pushed $name $digest"
  json+="$sep\"$name\":{\"ref\":\"$REF/agentbox/$name\",\"digest\":\"$digest\",\"harness\":\"$harness\"}"
  sep=","
done
echo "$json}" > "$OUT"
cat "$OUT"; echo
if docker inspect "$CONSOLE" >/dev/null 2>&1; then
  docker cp "$OUT" "$CONSOLE:/etc/agentbox/images.json"
  echo "copied images.json to $CONSOLE (restart the console to reseed the catalog)"
else
  echo "console container $CONSOLE not running; images.json left at $OUT"
fi

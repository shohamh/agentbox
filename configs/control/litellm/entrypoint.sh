#!/bin/sh
export LITELLM_MASTER_KEY="$(cat /secrets/litellm_master_key)"
exec litellm --config /app/agentbox-config.yaml --port 4000 --num_workers 1

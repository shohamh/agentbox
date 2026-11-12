#!/bin/bash
# Renders /etc/envoy/front.yaml for LITELLM_URL (default: the bundled LiteLLM,
# http://litellm:4000) and starts Envoy. The gateway sends model requests to the
# same LiteLLM the gatekeeper mints keys on, so one variable names both.
#
#   LITELLM_URL=https://litellm.corp.example            TLS on 443, system CAs
#   LITELLM_URL=https://litellm.corp.example:8443/llm   a port and a path prefix
#   LITELLM_CA_FILE=/ca/corp-ca.pem                     trust this CA bundle instead of the system roots
set -euo pipefail
url="${LITELLM_URL:-http://litellm:4000}"
scheme="${url%%://*}"
rest="${url#*://}"
hostport="${rest%%/*}"
prefix=""
[ "$rest" != "$hostport" ] && prefix="/${rest#*/}"
prefix="${prefix%/}"
host="${hostport%%:*}"
port="${hostport##*:}"
if [ "$port" = "$hostport" ]; then
  [ "$scheme" = https ] && port=443 || port=80
fi
case "$scheme" in http | https) ;; *) echo "LITELLM_URL must start with http:// or https://: $url" >&2; exit 1 ;; esac

tls=""
if [ "$scheme" = https ]; then
  ca="${LITELLM_CA_FILE:-/etc/ssl/certs/ca-certificates.crt}"
  tls="transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        sni: ${host}
        common_tls_context:
          validation_context:
            trusted_ca: {filename: ${ca}}
            match_typed_subject_alt_names:
            - san_type: DNS
              matcher: {exact: ${host}}"
fi

out=/tmp/front.yaml
{
  while IFS= read -r line; do
    case "$line" in
      *"# @@LITELLM_TLS@@"*) [ -n "$tls" ] && printf '    %s\n' "$tls" ;;
      *) line="${line//@@LITELLM_HOST@@/$host}"; line="${line//@@LITELLM_PORT@@/$port}"; line="${line//@@LITELLM_PREFIX@@/$prefix}"; printf '%s\n' "$line" ;;
    esac
  done < /etc/envoy/front.yaml
} > "$out"
echo "front-envoy: model requests go to ${scheme}://${host}:${port}${prefix}" >&2
exec /docker-entrypoint.sh -c "$out" --log-level warn "$@"

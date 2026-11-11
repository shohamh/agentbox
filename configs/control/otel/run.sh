#!/bin/sh
# Supervisor: run the collector with base.yaml plus the console-managed
# exporters file, and restart it when that file changes. If the merged
# configuration does not start, fall back to base.yaml alone until the file
# changes again, so a bad Settings edit cannot stop telemetry.
DYN=/dynamic/exporters.yaml
mkdir -p /dynamic
[ -s "$DYN" ] || printf 'exporters:\n  {}\n' > "$DYN"
pid=""
trap '[ -n "$pid" ] && kill $pid 2>/dev/null; exit 0' TERM INT
sum() { md5sum "$DYN" | cut -d' ' -f1; }
while true; do
  cur=$(sum)
  start=$(date +%s)
  /otelcol-contrib --config /etc/otelcol/base.yaml --config "$DYN" &
  pid=$!
  failed=0
  while kill -0 $pid 2>/dev/null; do
    sleep 3
    if [ "$(sum)" != "$cur" ]; then
      echo "exporters changed; restarting collector"
      kill $pid; wait $pid 2>/dev/null
      continue 2
    fi
  done
  wait $pid 2>/dev/null
  if [ $(( $(date +%s) - start )) -lt 15 ]; then
    echo "collector exited right after start with $DYN; falling back to base.yaml"
    /otelcol-contrib --config /etc/otelcol/base.yaml &
    pid=$!
    while kill -0 $pid 2>/dev/null; do
      sleep 3
      if [ "$(sum)" != "$cur" ]; then kill $pid; wait $pid 2>/dev/null; break; fi
    done
  fi
  sleep 1
done

# Telemetry backlog

**Trigger:** collector queue depth alert, or the Activity timeline lagging behind real time.

1. Check the central collector: `docker compose -p agentbox logs --tail 100 otel-gateway`. Look for "sending_queue is full" or exporter errors.
2. Check ClickHouse: it is the usual bottleneck. Inspect merges and parts:
   `SELECT table, count() FROM system.merges GROUP BY table` and `SELECT count() FROM system.parts WHERE active AND table='otel_logs'`.
   Too many small parts means inserts are too small or merges are behind; let merges finish, then raise the batch size in the collector.
3. Check the exporters in Settings: a failing `otlphttp` or `splunk_hec` destination backs up the queue because the pipeline waits on it.
   Use the test-send button; disable the destination if it is down (the change is audited and rewrites the collector's exporter list).
4. Scale the gateway tier (more `otel-gateway` replicas in production), then confirm the queue drains.
5. Meanwhile the appliances buffer locally: the appliance collector keeps up to 2 GB on the data disk and drops the lowest-priority events first; security events are kept last.
   Nothing is lost from a short backlog.
6. Retention: Settings, retention (full events 30 days, metadata one year). Lowering it applies a ClickHouse TTL.

Automated check: E2E-15 (telemetry tier down and recovered).

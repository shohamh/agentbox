# Add Splunk or another SIEM

**Trigger:** project request. No code change is needed: telemetry is OpenTelemetry end to end.

1. Get the destination: for Splunk, the HEC endpoint (`https://splunk.corp:8088/services/collector`), a HEC token and the index; for anything else, an OTLP/HTTP endpoint.
2. Console, Settings, Exporters (admin):
   - Splunk: enable `splunk_hec`, enter the endpoint, token and index (default `agentbox`);
   - other backends: enable `otlphttp` and enter the endpoint.
3. Press the test-send button for that exporter. The console sends one record straight to the destination and shows the result. Fix the endpoint, token or TLS trust until it succeeds.
4. Save. The console rewrites the central collector's exporter list (the file named by `COLLECTOR_DYNAMIC_CONFIG`); the collector applies it on restart
   (`docker compose -p agentbox restart otel-gateway` in the proof of concept; production tracks it through Git and Argo CD).
   The pipelines keep ClickHouse and add the new exporter, so the console and Grafana are unaffected.
5. Verify in the SIEM: search for `event.name=agentbox.exporter_test`, then for real events (`model.call`, `process.exec`, `audit.*`).
   Every record carries `agentbox.user`, `agentbox.device`, `agentbox.sandbox`, `agentbox.image`, `agentbox.bundle_version`; model calls carry the `gen_ai.*` attributes;
   admin actions arrive as `audit.<action>` records with `audit.actor`.
6. Check there are no gaps: counts of `model.call` in the SIEM and in ClickHouse over the same window should match (the UI-11 test does exactly this against a stand-in).
7. Record the change: it is in Audit as `settings.exporters` with the token masked.

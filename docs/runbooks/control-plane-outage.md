# Control plane outage

**Trigger:** gateway or verifier down (service alert), or users report "model service unreachable".

What users see: sandboxes keep running with their last policy bundle. Model calls fail with a clear error; `agentbox status` shows
`control UNREACHABLE (sandboxes keep running; model calls fail until it recovers)`. Non-model permissions stay valid until the bundle expires (24 h; v1 adds a 72 h grace).
The console holds no runtime state the gateway needs, so a console outage alone does not stop enforcement or model calls.

1. Find what is down: `docker compose -p agentbox ps`, then `docker compose -p agentbox logs --tail 100 <service>` for each unhealthy service.
   Dependencies in order: postgres, clickhouse, keycloak, stepca, litellm, gatekeeper, front-envoy, permsvc, console, otel-gateway.
2. Restore: `docker compose -p agentbox up -d <service>`. LiteLLM takes a minute or more to start.
3. Effects by service:
   - gatekeeper or front-envoy down: model calls fail and are never allowed through (fail closed);
   - permsvc down: `agentbox allow` fails with "permission service unreachable"; model calls still work;
   - litellm down: gatekeeper authorizes but the call fails with a clear error from the harness;
   - console down: only the web UI, enrollment and attestation are affected; appliances retry;
   - otel-gateway down: appliances buffer events on disk and send them afterwards.
4. **Verify recovery.** Devices: every PC `pass` and online again; appliances re-attest within one interval (at most about 20 minutes with one-hour certificates; within 10 minutes target in v1).
   `agentbox status` on a PC shows `control reachable`; a model call from a sandbox returns 200.
5. If many PCs show `fail` after the outage, check time and certificates first (see [attestation-failure-many.md](attestation-failure-many.md)).

Automated checks: E2E-09 and E2E-11 to E2E-15.

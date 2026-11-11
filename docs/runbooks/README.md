# Operations runbooks

One person is on call at a time, weekly. Only D1-D4, D8 and service outages page outside working hours.
Each alert in the console links to the runbook below; start from the evidence on the alert, then follow the steps.

| Runbook | Trigger | File |
|---|---|---|
| Enrollment fails | User report, or an `enroll` error | [enrollment-fails.md](enrollment-fails.md) |
| Attestation failure on one PC | D3 | [attestation-failure-one.md](attestation-failure-one.md) |
| Attestation failures across many PCs | D3 burst | [attestation-failure-many.md](attestation-failure-many.md) |
| Suspected tampering | D1, D2, tamper-matrix hits | [suspected-tampering.md](suspected-tampering.md) |
| Relay attempt | D4, D5 | [relay-attempt.md](relay-attempt.md) |
| Destructive activity | D8 | [destructive-activity.md](destructive-activity.md) |
| Suspected model backdoor trigger | D6, D7 clusters on one model | [model-backdoor.md](model-backdoor.md) |
| Appliance release rollout | Release approved | [release-rollout.md](release-rollout.md) |
| Control plane outage | Gateway or verifier down | [control-plane-outage.md](control-plane-outage.md) |
| Telemetry backlog | Collector queue alert | [telemetry-backlog.md](telemetry-backlog.md) |
| Flagged-change queue | Daily | [flagged-queue.md](flagged-queue.md) |
| Add Splunk or another SIEM | Project request | [add-splunk.md](add-splunk.md) |
| Key rotation | Calendar, or suspected exposure | [key-rotation.md](key-rotation.md) |

Detections implemented in the proof of concept: D3 (attestation failure), D4 (token relay), D5 (preview relay),
D6 (unexplained execution of network tools), D7 (denied egress), D10 (reviewer judged a flagged change unusual).
D1, D2 and D8 arrive with the host sensors and the full correlator in v1; their runbooks describe the manual steps until then.

Common tools: the console (Devices, Sandboxes, Requests, Activity, Alerts, Kill switches, Audit, Settings),
`agentbox status` and `agentbox doctor` on the PC, and `docker compose -p agentbox logs <service>` on the control-plane server.
Every console action needs a reason and is written to the audit log and to the OpenTelemetry stream.

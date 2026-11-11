# Testing

Every requirement has an automated test. The proof-of-concept suites are E2E-01..10, UI-01..12, the automated rows of the tamper matrix, and the
degraded-mode checks. All of them run against the real stack in containers.

## Running

```bash
scripts/test.sh                      # build, bring up, run everything, print the table, tear down
TEST=E2E-03 scripts/test.sh          # one test (E2E-nn, Tn, UI-nn; comma separated; E2E, DEG, TAMPER, UI for groups)
KEEP=1 TEST=UI-05 scripts/test.sh    # leave the stack up afterwards
STACK_UP=1 TEST=T7 scripts/test.sh   # reuse a running stack: only run the tests (fast loop)
SKIP_BUILD=1 scripts/test.sh         # skip scripts/release.sh (binaries, appliance image, signed manifests)
scripts/nightly.sh                   # three clean runs in a row (the phase-0 exit criterion)
```

`scripts/test.sh` runs `scripts/release.sh`, `scripts/up.sh` and `scripts/publish-images.sh`, then starts the appliances and PCs with
`docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml`, then runs two containers on the stack's network:

- `agentbox-tests`: the Go suite (`test/e2e`, build tag `e2e`, compiled by `test/Dockerfile`), with the docker socket mounted, driving the PCs with `docker exec`
  (`agentbox enroll`, `agentbox new`, `agentbox shell`, `codex exec`, `opencode run`) and asserting through the console API, ClickHouse and Postgres.
- `agentbox-ui-tests`: Playwright (`test/ui`, `test/ui/Dockerfile`), logging in through Keycloak as bob (admin), erin (second admin), carol (approver), frank (viewer).

Results land in `test/results/` (`go.log`, `ui.log`, `ui/` with the Playwright report and traces, `summary.txt`, `records.txt`). The table printed at the end looks like the plan's:

```
RESULT ID       TIME      TEST
PASS   E2E-03   41.2s     TestE2E_03_CodexTask
PASS   UI-05    96.8s     revoking a device stops model calls within 30 s; ...
```

Profiles: `tamper` (appliance-t + pc-t, device pc-t) is needed by E2E-10, T1 and T2; `dev` (the dev-flavor appliance; `DEV_PC`, `DEV_APPLIANCE`, `DEV_USER` name its
containers, default pc-d / appliance-d / alice) by T3. `scripts/test.sh` starts them when the selection needs them; tests skip with a message when they are absent.
Full runs start about twenty containers; on a machine with limited RAM stop other stacks first.

Quick checks without the stack: `scripts/go.sh vet -tags e2e ./test/...` and, in `test/ui`, `npx tsc --noEmit` (the UI Dockerfile runs it at build).

Knobs: `PERF_STRICT=1` makes the performance targets (P1, P3, shell latency) fail the run instead of warning; `STRICT_MESSAGES=1` requires the exact
"model service unreachable" text where the plan promises a clear message; `E2E05_SOAK_SECS` sets the soak length (default 45 s; the 30-minute real-work session stays manual);
`SAMPLE_REPO` names the sample repository; `SPLUNK_HEC_URL` / `SPLUNK_HEC_TOKEN` enable the Splunk part of UI-11.

## End-to-end tests (Go, Linux appliances; Windows rigs run the same plan on bare metal)

| ID | What it does | Passes when |
|---|---|---|
| E2E-01 | `agentbox enroll` as alice on pc-1 | Device enrolled and attested in the console within 60 s, evidence and audit entry present |
| E2E-02 | Same on a second Linux PC (pc-2, dave); stands in for the deb/rpm install | Same |
| E2E-03 | `agentbox new` with the Codex image, `codex exec` adds a unit test and runs the suite | Test exists and passes in the sandbox; model, tool and process events under alice in ClickHouse and the console timeline |
| E2E-04 | Same with `opencode run` | Same result; the same `agentbox.*` / `gen_ai.*` fields on `model.call` as Codex |
| E2E-05 | Remote-SSH config via `agentbox code`, shell round trips, soak, gateway overhead (P3) | ssh works through the ProxyCommand; latency within targets (warning unless `PERF_STRICT=1`) |
| E2E-06 | Search the sandbox, appliance and PC (environment, files, process memory) for the LiteLLM master key and any `sk-` key; replay the sandbox token from the appliance host, another sandbox and another PC's appliance | No key anywhere; replays get 403; `token.relay` event and a D4 alert |
| E2E-07 | `codex exec -s workspace-write` (bubblewrap) under gVisor | Result recorded in the log (`RECORD`), never fails; `danger-full-access` stays the default unless it works |
| E2E-08 | Agent tries a host outside its egress list | Blocked; `egress.denied` event, D7 alert, timeline entry |
| E2E-09 | Cut the appliance off from the control plane mid-task (nftables), then restore | Sandboxes keep running; model calls fail with a message; status reports the outage; the PC recovers on its own (O3) |
| E2E-10 | Enroll pc-t, whose appliance image is modified | Console shows failed attestation with the diff and a D3 alert; no sandbox or model access |

Degraded mode (plan: offline operation table): E2E-11 permission service down, E2E-12 gateway authorizer down (fails closed), E2E-13 LiteLLM down (no harness hang),
E2E-14 console down (enforcement continues), E2E-15 telemetry tier down (appliance events buffered and delivered afterwards).

## Tamper matrix (automated rows)

| ID | Attempt | Test |
|---|---|---|
| T1 | Modify a binary in the appliance root | pc-t attestation fails; the diff names the modified file |
| T2 | Boot a modified UKI or kernel command line | PCR 11 matches no release; no identity |
| T3 | Boot the dev flavor | Refused by the production verifier |
| T7 | Replay an old quote | Reused, expired, never-issued and earlier real nonces fail the nonce check |
| T9 | Use the sandbox token from the host or another sandbox | 403 and a D4 alert |
| T10 | Run a model-API proxy on a preview port | 403 "model-API-shaped" and a D5 alert |
| T12 | Edit a policy bundle on disk | The edited bundle is not applied after restart; the published bundle is |

Manual per release: T4 and T5 (debugger against QEMU, strict and non-strict Linux), T6 (LiveCloudKd / MemProcFS on Windows), T8 (copy the VM to another PC), T11 (roll back to a revoked image).
Not reproducible in containers: dm-verity (T1 here is the equivalent measured-file change), real vTPM binding to a physical TPM, Hyper-V.

## Console tests (Playwright, UI-01..12)

Each is also walked through once by hand by the requester. Test titles start with the ID, so a run reads like the plan's table.

| ID | What it does |
|---|---|
| UI-00 | Smoke: every page loads, overview counters visible |
| UI-01 | `agentbox allow` for an automatic-list host: applied within 10 s, shown as automatic in Requests and Audit |
| UI-02 | Approver approves one request (reaches the sandbox within 10 s) and denies another (CLI shows the reason) |
| UI-03 | Flagged change: reviewer verdict appears; admin confirms one and rolls one back |
| UI-04 | Stop and recycle a sandbox during a Codex task: stops within 5 s, CLI shows who and why |
| UI-05 | Revoke the device: model calls fail within 30 s; re-enrolling restores access |
| UI-06 | Per-model kill switch for everyone within 30 s, other models unaffected; the global switch needs a second admin |
| UI-07 | Budget below use: Codex and opencode print a budget error, no hang |
| UI-08 | Edit and publish the group default: every online appliance reports the new bundle within 60 s |
| UI-09 | Every tool call has matching process events; model-call counts equal LiteLLM's spend log |
| UI-10 | Every action above is in the audit log with the actor and in ClickHouse as an `audit.*` OTel record (run last) |
| UI-11 | `otlphttp` exporter to the test collector (`sink`): same records, no gaps; Splunk HEC when `SPLUNK_HEC_URL` is set |
| UI-12 | Viewer and approver accounts can do only what their role allows (UI and API) |

## Contracts the suites rely on

- Compose project `agentbox`; services `postgres clickhouse keycloak stepca litellm mockllm gatekeeper front-envoy otel-gateway permsvc reviewer correlator console grafana registry sink corp-services`,
  `appliance-1`/`pc-1` (alice), `appliance-2`/`pc-2` (dave), profile `tamper` (`appliance-t`, `pc-t`), profile `dev`.
- Console at `http://console.agentbox.corp:18080`, Keycloak at `http://kc.agentbox.corp:8080`, users bob/erin (admin), carol (approver), frank (viewer), alice, dave; all `Passw0rd!`.
- `correlator` holds `CLICKHOUSE_URL/USER/PASSWORD`, `permsvc` holds `DATABASE_URL`, `gatekeeper` holds `LITELLM_DATABASE_URL`; the PC containers hold `/etc/agentbox/host.json`, the appliances `/etc/agentbox/appliance.json`.
- The sandbox images contain `python3`, `codex`, `opencode`, an sshd for `root` accepted with the user's key, and source `/etc/agentbox/env` in ssh sessions or the tests source it themselves.
- The `sink` service answers `GET /stats` with `{"log_records": n, "by_event": {"<event.name>": n}}` and counts OTLP/HTTP logs by `event.name`.

## Running the E2E suite against a native Windows PC

`scripts/build-windows.sh` produces `dist/windows/e2e.exe`. With the Windows
host installed (`scripts/install-windows.ps1`), the control plane up, and the
`windows.yml` overlay applied, run it natively:

```powershell
$env:AGENTBOX_EXE = "$env:LOCALAPPDATA\Agentbox\bin\agentbox.exe"
$env:E2E_PC1 = "pc-win"; $env:E2E_APP1 = "appliance-w"; $env:STRICT_MESSAGES = "1"
$env:E2E_HOSTMAP = "kc.agentbox.corp:8080=127.0.0.1:18180,console.agentbox.corp:18080=127.0.0.1:18080,clickhouse:8123=127.0.0.1:18123"
dist\windows\e2e.exe -test.v -test.run "^TestE2E_0[1-9]_|^TestE2E_1[1-5]_|^TestT9_|^TestT10_|^TestT12_"
```

`E2E_HOSTMAP` redirects TCP destinations only (URLs and Host headers are kept),
because the PC cannot resolve `*.agentbox.corp` without editing its hosts file.
Steps that ran shell commands on the simulated PC have native variants in
`test/e2e/native_test.go`.

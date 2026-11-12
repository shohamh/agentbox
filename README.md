# Agentbox (proof of concept, container test bed)

Sandboxed AI coding agents on the user's own PC, with model access only from an
attested appliance and a central control plane for identity, policy, the model
gateway, the admin console and telemetry. This repository implements phase 0 of
the build plan and runs end to end on Docker.

## Guides

| For | Read |
|---|---|
| Developers using the sandboxes (Claude Code, Codex, opencode, omp, Orca, your own agent) | [docs/user-guide.md](docs/user-guide.md) |
| The people who run the company LiteLLM (short-lived per-sandbox keys, readiness check) | [docs/litellm-admin.md](docs/litellm-admin.md) |
| Whoever deploys it on the company network: what is ready, what to replace, the order of work | [docs/deployment.md](docs/deployment.md) |
| Windows PCs with Hyper-V | [docs/hyperv.md](docs/hyperv.md) |
| Operators | [docs/runbooks](docs/runbooks/README.md), [docs/testing.md](docs/testing.md) |

## What runs

| Part | Where | Notes |
|---|---|---|
| `agentbox` CLI, `agentbox-host` | PC | Linux and Windows builds (Windows is cross-compiled only) |
| `agentbox-manager`, Envoy, OTel Collector, gVisor | appliance | per-task gVisor sandboxes, signed policy bundles, egress proxy, DNS allowlist |
| Keycloak, step-ca, Postgres, ClickHouse, LiteLLM, front Envoy, gatekeeper, permsvc, reviewer, correlator, console, Grafana | control plane | `deploy/compose` |
| `mockllm` | stands in for vLLM | deterministic OpenAI-compatible server, drives Codex CLI, opencode, Claude Code and omp through real tool calls |

In the test bed the appliance is a privileged container, the TPM is swtpm, and
vsock is a private TCP network. The Hyper-V driver has run on a real Windows PC
(`docs/hyperv.md`); the QEMU/KVM driver boots under QEMU in tests.

## Run it

Needs Docker with about 4 GB of memory. Everything builds in containers; no Go or Node on the host.

```bash
scripts/test.sh                 # build, bring up, run every suite, print a pass/fail table, tear down
KEEP=1 scripts/test.sh          # leave the stack running afterwards
TEST=E2E-03 scripts/test.sh     # one test (E2E-nn, Tn, UI-nn, E2E, DEG, TAMPER, UI)
```

Or step by step: `scripts/build-bins.sh`, `scripts/bootstrap.sh`, `scripts/release.sh`,
`scripts/up.sh`, `scripts/publish-images.sh`, then
`docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml --env-file deploy/compose/.secrets/compose.env up -d`.

Console: http://console.agentbox.corp:18080 (add `127.0.0.1 console.agentbox.corp kc.agentbox.corp`
to your hosts file; Keycloak must be reachable under the same name as the console's
sign-in redirect). Users `bob`, `erin` (admin), `carol` (approver), `frank` (viewer), password `Passw0rd!`.

Use a PC:

```bash
echo 'Passw0rd!' | docker exec -i agentbox-pc-1-1 agentbox enroll --user alice --password-stdin
docker exec agentbox-pc-1-1 agentbox status
docker exec agentbox-pc-1-1 agentbox new --name demo --image codex
docker exec agentbox-pc-1-1 agentbox shell demo -- 'cd /workspace && codex exec --skip-git-repo-check "add a unit test for calc.mul and run the suite"'
```

## Layout

`cmd/` Go services and tools, `pkg/` shared packages, `web/console` React UI,
`configs/` Envoy, OTel, Keycloak, LiteLLM and appliance config, `deploy/compose`
the stacks, `image/` appliance, PC and corp-services images, `sandbox-images/`
base, Codex and opencode, `test/` E2E (Go) and console (Playwright) suites,
`docs/` runbooks and `docs/testing.md`.

## Known gaps (see `docs/testing.md`)

Light attestation only (virtual-TPM quote of PCR 11); no Keylime, winattest or
SPIRE yet. Not automated: T4 to T6, T8, T11, the 30-minute real-work session in
E2E-05, the Splunk half of UI-11, and the console UI suite against a Windows PC.
The Hyper-V appliance boots under QEMU with Secure Boot in tests, but has not
been booted on Hyper-V itself (see `docs/hyperv.md`).

## Windows PC, natively in a container-backed setup

The PC side also runs natively on Windows, without admin rights: `agentbox.exe`
talks to `agentbox-host.exe` over a named pipe, SSH goes through Windows' own
OpenSSH, and the appliance runs in Docker Desktop (a Hyper-V VM is the
production path and needs elevation plus a bootable appliance image, which this
repo does not build yet).

```powershell
bash scripts/build-windows.sh                       # dist\windows\{agentbox,agentbox-host,e2e}.exe
docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml `
  -f deploy/compose/windows.yml --env-file deploy/compose/.secrets/compose.env up -d console clickhouse appliance-w
scripts\install-windows.ps1                         # installs to %LOCALAPPDATA%\Agentbox, starts the host service
& "$env:LOCALAPPDATA\Agentbox\bin\agentbox.exe" status
scripts\install-windows.ps1 -Uninstall              # remove it again
```

The same E2E suite runs against it: set `AGENTBOX_EXE`, `E2E_PC1=pc-win`,
`E2E_APP1=appliance-w` and `E2E_HOSTMAP` (see `docs/testing.md`) and run
`dist\windows\e2e.exe -test.v`. The console UI suite (Playwright) is not run
against the Windows PC.

## Windows PC with a real Hyper-V appliance

`docs/hyperv.md`: a bootable appliance disk (Debian signed shim, GRUB and kernel;
Secure Boot; virtual TPM; Hyper-V sockets), the host service as a Windows
service, and an elevated installer, `scripts\install-hyperv.ps1`.

> Original build plan, exported from the Claude Docs artifact (https://claude.ai/artifact/4pypPESnCHPfb5PJQwiPW8, revision 32) on 2026-11-12. Two embedded diagrams could not be exported and are replaced by a text placeholder. This repository implements it; see [testing.md](testing.md) and [deployment.md](deployment.md) for what is verified.

# Agentbox Build Plan

Nov 10, 2026 · @A

## Summary

Agentbox v1 is a signed Linux appliance VM that runs locally on each user's Windows (Hyper-V) or Linux (QEMU/KVM) PC. Agents work inside per-task gVisor sandboxes in that local appliance, which is the only place model access works. Servers run only the control plane: identity and attestation, policy, the model gateway, the admin console and telemetry. Server-hosted sandboxes are a later addition that reuses the same image. Everything builds, installs, updates and runs with no internet connection. This plan turns the design in the security plan into buildable components, interfaces, a test matrix and a delivery schedule, starting with a proof of concept.

**What v1 delivers**

- One appliance image that runs locally on Windows and Linux PCs; the same image later runs on servers.
- Model access only from attested appliances, with the LiteLLM credential injected on the server side, so no key ever exists on a PC.
- Self-service, centrally managed permissions with flagged review by a model from a different family.
- Debug targets for kernel and driver developers.
- Detections for tampering and relaying, including matching each model-requested action to real activity in the sandbox.
- An admin console for administration and monitoring, with OpenTelemetry output ready for Splunk or any other OTLP backend.

**Decisions that shape the build**

- **One codebase in Go** for all custom parts, cross-compiled for Windows and Linux; reuse Keylime's Rust agent, SPIRE, Envoy, gVisor and OPA rather than writing security primitives.
- **Measured boot is the enforcement.** An appliance that boots anything other than the published image never gets an identity, so Secure Boot inside the VM is extra hardening, not the gate.
- **The key never leaves the server.** This refines the main plan: the front gateway attaches the LiteLLM key after checking the appliance's certificate.
- **Local first.** Agents run in a sandbox hosted on the user's own PC from the first release, so they work next to local files, tools and debug targets with no per-user server capacity. The server side only verifies, authorizes, injects the model key, manages policy and collects telemetry. Server-hosted sandboxes (assurance level A) are phase 2: same image, for teams that want them and for PCs that cannot pass attestation.

**Timeline and team.** With four engineers (Linux platform, Windows platform, identity and backend, console and telemetry) plus part-time security review: the proof of concept, a local sandbox on Windows and Linux with the admin console, ends around week 6; a pilot on Windows and Linux PCs with full attestation starts around week 16; organization-wide waves follow from week 20; server-hosted sandboxes are phase 2, with a pilot around week 28. These are planning estimates; revisit them after the proof of concept.

## Proof of concept (phase 0)

The proof of concept shows in about six weeks that agents work well inside a sandbox on the user's own PC, on both Windows and Linux. Model access goes only through that sandbox under the user's corporate identity. Admins can see and control everything from one web console, and all telemetry is OpenTelemetry. It uses the production appliance image and the same interfaces as v1, so most of it carries over. The simplifications are in identity and hardening.

### What the proof of concept includes

- **Local sandbox on both platforms.** The signed appliance runs on Windows 11 (Hyper-V) and on Ubuntu or Fedora (QEMU/KVM). It is installed by an MSI or a deb/rpm package and holds per-task gVisor sandboxes. The sandbox images carry Codex CLI, opencode and common toolchains.
- **The CLI.** It covers `enroll`, `login`, `up`, `new`, `list`, `shell`, `code`, `rm`, `allow`, `perms` and `status`.
- **The model path.** It runs from the local Envoy in the appliance to the front Envoy and gatekeeper, then to LiteLLM, then to the internal vLLM. The LiteLLM key is attached only on the server.
- **Permissions.** Self-service requests are sorted into three outcomes: applied automatically, needing approval, or allowed but flagged. A reviewer model from a different family checks the flagged ones.
- **Admin console.** It is described below.
- **OpenTelemetry pipeline.** It runs from the appliance to a central Collector and on to ClickHouse. The Splunk and OTLP exporters are configured and tested against stand-ins.
- **A single control-plane server.** It runs under Podman Compose with:
  - Keycloak for single sign-on, federated to the directory;
  - step-ca for device certificates;
  - Postgres, ClickHouse and Grafana;
  - Envoy, gatekeeper and LiteLLM;
  - agentbox-console.

### What is simplified, and what replaces it in v1

- **Attestation is light.** The appliance sends a virtual-TPM quote of PCR 11, and the console checks it against the release manifest. Host attestation (winattest and Keylime) and SPIRE arrive in v1.
- **Certificates.** Device certificates come from step-ca at enrollment. Appliance certificates last one hour instead of five minutes. Revocation still takes effect within 30 seconds, because gatekeeper checks a deny list.
- **Policy signing** uses an online key held by the console. The offline signing key arrives in v1.
- **Detections.** gVisor process and network events plus gateway logs are collected and shown. The correlator and the full detection set arrive in v1.
- **Not in phase 0:** debug targets, assurance-level automation, high availability and server-hosted sandboxes. Server-hosted sandboxes come in phase 2.

### What "with my login" means

Users sign in with their normal corporate account through Keycloak's device-code flow, inside `agentbox enroll` and `agentbox login`. The appliance's certificate then names that user and that device, and every model call, tool call and admin action is logged under that name.

Codex's ChatGPT sign-in and opencode's provider sign-in are not used. Both need the internet, and both would leave a credential on the PC. Instead, both tools point at the sandbox's local model endpoint with a per-sandbox token that is worthless outside the sandbox. LiteLLM spend, console views and audit entries all show the user's own name.

### Harness configuration baked into the sandbox images

Codex, in `~/.codex/config.toml`. gVisor is the sandbox, so Codex's own bubblewrap sandbox is off by default; test E2E-07 decides whether to turn it on.

```toml
model = "deepseek-v3"
model_provider = "agentbox"
sandbox_mode = "danger-full-access"   # the gVisor sandbox is the boundary

[model_providers.agentbox]
name = "Agentbox"
base_url = "http://llm.agentbox:4000/v1"
env_key = "AGENTBOX_TOKEN"            # per-sandbox token, set by agentbox-manager
wire_api = "responses"                # LiteLLM bridges the Responses API to vLLM

[otel]
exporter = { otlp-http = { endpoint = "http://otel.agentbox:4318/v1/logs", protocol = "binary" } }
log_user_prompt = true                # stays inside the org; needed for review
```

opencode, in `~/.config/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "agentbox": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Agentbox",
      "options": {
        "baseURL": "http://llm.agentbox:4000/v1",
        "apiKey": "{env:AGENTBOX_TOKEN}"
      },
      "models": {
        "deepseek-v3": { "name": "DeepSeek V3" },
        "glm-4": { "name": "GLM-4" }
      }
    }
  },
  "model": "agentbox/deepseek-v3",
  "autoupdate": false,
  "share": "disabled"
}
```

**Offline notes.**

- Preinstall the `@ai-sdk/openai-compatible` package in the image, or serve it from the internal npm mirror.
- Turn off opencode's model-catalog fetch from models.dev.
- Turn off update checks in both tools.
- Pin the Codex and opencode versions in the image. Their configuration keys change between releases, so the image build runs a smoke test against the pinned versions.

### Admin console

agentbox-console is a Go API with a React and TypeScript web UI:

- **Storage.** State is in Postgres and events are in ClickHouse.
- **Sign-in and roles.** Sign-in is through Keycloak. There are three roles: viewer, approver and admin.
- **Live updates.** Pages update live over server-sent events.
- **Audit.** Every admin action is written to the audit log and also emitted as an OpenTelemetry log record, so Splunk receives admin actions too.
- **Pushing changes.** Changes reach appliances through the same signed policy bundles as v1. In the proof of concept, appliances long-poll for them, so changes land within seconds.

| Page | What admins see | What admins can do |
| --- | --- | --- |
| Overview | Enrolled and online devices, live sandboxes, model calls per minute, open requests, open alerts | Jump to any item |
| Devices | Each PC with OS, appliance version, attestation verdict and evidence, bundle version, last contact | Revoke, force recycle, set assurance level |
| Users | Tier, devices, model usage against budget | Change tier or budget, suspend |
| Sandboxes | Live sandboxes per device with image, harness, mounts, egress and uptime | Stop, recycle, open the activity timeline |
| Requests | Permission requests with the reviewer model's verdict and reasoning | Approve, deny, approve with expiry, turn a pattern into an automatic rule |
| Policy | Group defaults, image catalog, model list, egress catalog, debug-lease rules (v1) | Edit, preview the diff per device, sign and publish |
| Activity | One timeline per sandbox: model calls, tool calls, processes, network connections, file sync | Search, filter, export, pin as evidence |
| Alerts | Detections with severity and linked evidence | Acknowledge, assign, escalate, revoke from the alert |
| Kill switches | State of the global, per-model and per-group switches | Toggle, with a required reason; global off needs a second admin |
| Audit | Every admin and system action | Search and export |
| Settings | Exporter destinations (OTLP, Splunk HEC), retention, single sign-on, reviewer model | Edit, with a test-send button per exporter |

### Telemetry: OpenTelemetry, with Splunk as configuration

**In each appliance.** An OpenTelemetry Collector (contrib build) receives OTLP from three sources:

- gVisor runtime monitoring, through a small adapter;
- Codex's built-in OTel export;
- agentbox-manager.

It also tails Envoy access logs. It stamps every record with user, device, sandbox, image and bundle version, then sends over mutual TLS to the central tier. A disk-backed queue covers network outages.

**Central tier.** A gateway Collector writes to ClickHouse, which the console and Grafana read. Model-call records use the GenAI semantic conventions (`gen_ai.*`), and Agentbox-specific fields sit under `agentbox.*`.

**Adding Splunk later** means two things: add the `splunk_hec` exporter, which carries logs, metrics and traces, and list it in the pipelines. Any other OTLP backend uses `otlp` or `otlphttp` the same way.

```yaml
receivers:
  otlp:
    protocols:
      grpc: { endpoint: 0.0.0.0:4317 }   # mutual TLS settings omitted
processors:
  memory_limiter: { check_interval: 1s, limit_percentage: 80 }
  batch: {}
exporters:
  clickhouse:
    endpoint: tcp://clickhouse:9000
    database: agentbox
  # splunk_hec:
  #   endpoint: https://splunk.corp:8088/services/collector
  #   token: ${env:SPLUNK_HEC_TOKEN}
  #   index: agentbox
  # otlphttp/siem:
  #   endpoint: https://siem.corp:4318
service:
  pipelines:
    logs:    { receivers: [otlp], processors: [memory_limiter, batch], exporters: [clickhouse] }
    traces:  { receivers: [otlp], processors: [memory_limiter, batch], exporters: [clickhouse] }
    metrics: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [clickhouse] }
```

### End-to-end tests

**Test bed:**

- one Windows 11 Pro PC with Hyper-V;
- one Ubuntu 24.04 PC with KVM;
- the control-plane server;
- the internal LiteLLM and vLLM serving DeepSeek and GLM, or a small model for nightly runs.

The test account is the requester's own corporate account. A Go test harness drives the CLI over WinRM and SSH. It runs the harnesses non-interactively with `codex exec` and `opencode run`, and Playwright drives the console. Every test runs on both operating systems.

| ID | Test | Passes when |
| --- | --- | --- |
| E2E-01 | Install the MSI on Windows, run agentbox enroll and sign in | The device shows as enrolled and attested in the console within 60 s |
| E2E-02 | Same on Linux with the deb or rpm | Same |
| E2E-03 | agentbox new with the Codex image on a sample repo, then codex exec to add a unit test and run the suite | The test exists and passes inside the sandbox, and the console shows the model calls, tool calls and processes under the user's name |
| E2E-04 | The same task with opencode run | Same result and the same telemetry fields |
| E2E-05 | Interactive Codex and opencode sessions in VS Code over Remote-SSH | Usable for 30 minutes of real work; added latency within P3 |
| E2E-06 | Search the sandbox's environment, files and memory for the LiteLLM key, then replay the local token from the host and from another PC | No key anywhere; the token is refused outside its sandbox and raises a relay alert |
| E2E-07 | Run Codex with its own workspace-write sandbox (bubblewrap) under gVisor | The result is recorded; danger-full-access stays the default unless it works |
| E2E-08 | The agent tries a host outside its egress list | Blocked, with an alert and a timeline entry |
| E2E-09 | Cut the PC off from the control plane mid-task, then restore it | Sandboxes keep running, model calls fail with a clear message, and the PC recovers on its own (O3) |
| E2E-10 | Boot a modified appliance image | The console shows failed attestation, and model access is refused |

### Management tests from the console

Each test is automated in Playwright and also walked through once by hand by the requester.

| ID | Test | Passes when |
| --- | --- | --- |
| UI-01 | The user runs agentbox allow for an egress host on the automatic list | Applied within 10 s and shown as automatic in Requests and Audit |
| UI-02 | The user asks for a change that needs approval; an admin approves it, then a second one is denied | The approved change reaches the sandbox within 10 s; the denial reaches the CLI with the admin's reason |
| UI-03 | The user makes an allowed-but-flagged change | The reviewer's verdict appears; the admin can confirm it or roll it back |
| UI-04 | Stop and recycle a sandbox from the console during a Codex task | It stops within 5 s, and the CLI shows who stopped it and why |
| UI-05 | Revoke the device | Model calls fail within 30 s; re-enrolling restores access |
| UI-06 | Turn on the kill switch for one model | Calls to that model fail for everyone within 30 s; other models keep working |
| UI-07 | Lower a user's budget below current use | Codex and opencode get a clear budget error, not a hang |
| UI-08 | Edit a group's default policy and publish it | Every online appliance reports the new bundle version within 60 s |
| UI-09 | Check the activity timeline against E2E-03 and E2E-04 | Every tool call has matching process or network events, and model-call counts match LiteLLM's spend log |
| UI-10 | Check the audit log after all of the above | Every action is listed with the admin's name and appears in ClickHouse as an OTel log record |
| UI-11 | Turn on an otlphttp exporter to a test Collector, and splunk\_hec to a Splunk test instance if one is available | Records arrive with the same attributes and no gaps |
| UI-12 | Try approve and publish as viewer and approver accounts | Each role can do only what it is allowed to |

### Exit criteria

- All E2E and UI tests pass on both operating systems three nightly runs in a row.
- Five to ten volunteer R&D users work in it for a week and get from install to a first working agent in under 10 minutes. Most of them choose to keep using it.
- Warm sandbox start is under 10 s and model-path overhead is under 50 ms (P1, P3).
- A written decision on Codex's inner sandbox mode, a list of toolchains that misbehave under gVisor, and confirmation that Hyper-V can be enabled across the Windows fleet.

### Schedule

| Week | Work |
| --- | --- |
| 1 | Compose stack with Keycloak, step-ca, LiteLLM, Postgres, ClickHouse and Grafana; console skeleton with sign-in; appliance image booting on Hyper-V and QEMU |
| 2 | Host service and CLI on both operating systems; enrollment; vsock control channel; sandbox create, list and remove |
| 3 | Model path with local token and server-side key; Codex and opencode images; OpenTelemetry pipeline into ClickHouse |
| 4 | Console pages for devices, users, sandboxes, requests, policy and kill switches; the permission flow end to end |
| 5 | Activity timeline, alerts, audit, light attestation; automated E2E and UI suites in CI |
| 6 | Volunteer week, fixes, exit review and go or no-go for v1 |

## Requirements and non-goals

Every requirement below is testable; the test matrix later in this plan maps to these IDs.

### Functional

| ID | Requirement |
| --- | --- |
| F1 | Runs on Windows 11 Pro or Enterprise (23H2 or later) with Hyper-V, and on Linux PCs with KVM, without Docker Desktop or WSL |
| F2 | The same appliance image runs locally on Hyper-V and QEMU/KVM PCs; server hosting (level A, phase 2) reuses it unchanged |
| F3 | `agentbox new` gives a ready sandbox with a preconfigured agent harness and repository |
| F4 | Sandboxes reach the model through `llm.agentbox:4000` with OpenAI- and Anthropic-compatible APIs; no real credential is visible inside |
| F5 | Users open sandboxes in VS Code Remote-SSH, JetBrains Gateway or a terminal, and preview web apps from the PC |
| F6 | Project folders sync between PC and sandbox |
| F7 | Users request permission changes from the CLI or tray; changes apply within seconds |
| F8 | Driver developers lease debug targets (local VMs, lab VMs, physical rigs) and attach debuggers manually or through agents |
| F9 | A host-side assist key gives editor completion and chat without tool calling |
| F10 | An admin web console manages devices, users, sandboxes, permission requests, policy, kill switches and debug leases, and shows attestation state, model usage, alerts and the audit log |
| F11 | All telemetry is OpenTelemetry (OTLP) and can be sent to Splunk or another OTLP backend by configuration alone |
| F12 | Codex CLI and opencode run unmodified inside a sandbox, attributed to the signed-in user, with no model credential in their configuration |

### Security

| ID | Requirement |
| --- | --- |
| S1 | No model access without a passing attestation of both the PC and the appliance |
| S2 | Model access stops within 5 minutes of any attestation failure or revocation |
| S3 | No model credential exists on any PC, inside the appliance or in a sandbox |
| S4 | Sandboxes cannot reach networks, files or devices beyond their signed policy |
| S5 | Code in a sandbox cannot read or alter the appliance's identity material, policy or telemetry |
| S6 | Every model call, tool call, permission change and attestation result is logged with user, device, sandbox and model version |
| S7 | Relay attempts through ports or synced files are detected within one day, and model-API-shaped port traffic is blocked |
| S8 | Tamper signals trigger automatic revocation and appliance restart |

### Offline

| ID | Requirement |
| --- | --- |
| O1 | Build, sign, install, enroll, update and run with no internet access at any stage |
| O2 | No runtime dependency on public certificate authorities, transparency logs, symbol servers or vendor clouds |
| O3 | PCs keep working sandboxes (without model access) if the control plane is unreachable, and recover automatically |

### Performance targets

| ID | Target |
| --- | --- |
| P1 | Warm sandbox ready in under 10 s at p95 |
| P2 | Appliance cold boot to first model call in under 60 s at p95 |
| P3 | Local Envoy and front-gateway overhead under 50 ms per model request at p95 |
| P4 | Typical build workloads in a sandbox no more than 30% slower than native; heavier I/O work runs on a debug target VM |
| P5 | Appliance idle footprint under 2 GB RAM on the PC |

### Non-goals for v1

macOS hosts; Windows Home; GPU passthrough into sandboxes; protection against attackers with physical access to TPM buses; confidential computing on PCs; replacing existing EDR.

## Key design decisions

Each decision is the default for v1; revisit only with test evidence.

| Area | Decision | Why | Alternative considered |
| --- | --- | --- | --- |
| Language | Go for all custom code; static binaries | One toolchain for Windows and Linux, strong TPM, SPIFFE and Envoy libraries, easy offline module mirroring | Rust (better memory safety, weaker ecosystem for SPIRE plugins and Windows WMI) |
| Windows hypervisor | Hyper-V Gen 2 VM, driven by the host service | Built in, virtual TPM and Secure Boot for guests, Hyper-V sockets | WSL2 (no virtual TPM; its lock-down policies cover only Store WSL) |
| Linux hypervisor | QEMU/KVM with swtpm and OVMF | Most complete: virtual TPM, Secure Boot with custom keys, vsock, virtio-fs | Cloud Hypervisor (lighter; adopt later once its TPM and UEFI support is validated) |
| Host-to-appliance channel | vsock everywhere (Hyper-V sockets on Windows, vhost-vsock on Linux) | No IP networking needed for control; one protocol in the guest | A host-only virtual network (more firewall surface) |
| Sandbox runtime | gVisor `runsc`, systrap platform, called directly from OCI bundles | Strong isolation without KVM inside the VM; no container daemon | Kata or Firecracker inside the appliance (needs nested virtualization on PCs) |
| Appliance OS | Minimal Debian or Fedora image built with mkosi: unified kernel image, dm-verity read-only root, A/B partitions | Reproducible, measurable, small; systemd tooling precomputes boot measurements | A general-purpose distro install (unmeasurable drift) |
| Enforcement root | Measured boot and remote attestation; Secure Boot in the VM as hardening | Works the same on Hyper-V and QEMU even where custom Secure Boot keys are awkward | Secure Boot as the gate (Hyper-V client templates limit custom keys) |
| Attestation | Keylime for appliances and Linux hosts; custom winattest on go-attestation for Windows hosts | Keylime is mature and continuous; nothing open source covers Windows boot-log policy | Writing one verifier for everything (more custom security code) |
| Identity | SPIRE with a custom node-attestor plugin that joins host and appliance verdicts | Short-lived certificates, Envoy integration over SDS, revocation by eviction | Issuing certificates directly from a custom CA service |
| Model credential | Injected on the server by the front gateway after mutual TLS; PCs never hold a key | Removes the copy-the-key problem entirely | Short-lived keys minted to the appliance (the main plan's first design) |
| Policy format | JSON bundles signed with an offline-held key (minisign or keyed cosign), 24-hour expiry | Simple to verify in Go; works without transparency logs | OPA bundles pushed live (needs constant connectivity) |
| Sandbox telemetry | gVisor runtime monitoring into the appliance's OpenTelemetry Collector | Host-level eBPF cannot see inside gVisor | Tetragon inside sandboxes (not possible under gVisor) |
| Control plane | Kubernetes (RKE2 or k3s, air-gapped install) with Argo CD; the proof of concept runs on one server with Podman Compose | Repeatable offline deployment, HA, GitOps | Plain VMs with systemd (fewer moving parts, harder HA) |
| Where sandboxes run | On the user's PC, inside the local appliance VM; servers host only the control plane | Lowest latency, direct use of local files, devices and debug targets, no server capacity per user, and R&D keeps working on its own machine | Server-hosted sandboxes first (needs per-user server capacity and remote file sync; now phase 2) |
| Telemetry pipeline | OpenTelemetry end to end: a Collector in each appliance, a central Collector tier, OTLP between them | One format for logs, metrics and traces; Splunk later through the splunk\_hec exporter, other backends through otlp or otlphttp, with configuration changes only | Vector (capable, but less standard for traces and vendor hand-off) |
| Admin console | agentbox-console: Go API and a React web UI on Postgres, reading events from ClickHouse; Grafana for ad hoc dashboards | One place to see and act: approvals, revocations, policy and leases next to the evidence for them | Grafana plus permsvc's page only (read-only views, split admin experience) |

## Components and repository layout

Eight custom components, a set of configurations for reused open-source parts, and an image recipe. Everything lives in one monorepo so a single tagged release produces every artifact.

| Component | Runs on | Built by | Workstream |
| --- | --- | --- | --- |
| `agentbox-host` (service) and `agentbox` (CLI, tray) | Windows and Linux PCs | Custom, Go | Windows platform / Linux platform |
| `agentbox-manager` | Inside the appliance | Custom, Go | Linux platform |
| Appliance image | Hyper-V and QEMU on PCs (server KVM in phase 2) | mkosi recipe | Linux platform |
| `winattest` (verifier) | Control plane | Custom, Go on go-attestation | Identity and backend |
| `spire-plugin-agentbox` (node attestor, agent and server halves) | Appliance and control plane | Custom, Go, SPIRE plugin SDK | Identity and backend |
| `gatekeeper` (front-gateway authorization and key injection) | Control plane, beside front Envoy | Custom, Go | Identity and backend |
| `permsvc`, `reviewer` and policy signer | Control plane | Custom, Go | Identity and backend |
| `correlator` (tool-call to execution matching) | Control plane | Custom, Go | Identity and backend |
| agentbox-console (admin API and web UI) | Control plane | Custom, Go and React (TypeScript) | Console and telemetry |
| Reused: Keylime, SPIRE, Envoy, gVisor, OPA, LiteLLM, Falco, OpenTelemetry Collector, ClickHouse, Grafana, Wazuh, Fleet | Various | Configuration only | All |

### Repository layout

```text
agentbox/
  cmd/
    agentbox/            CLI (Windows, Linux)
    agentbox-host/       host service (Windows service, systemd unit)
    agentbox-manager/    appliance daemon
    agentbox-console/    admin API, approvals, kill switches, audit
    winattest/           Windows host verifier
    gatekeeper/          front-gateway ext_authz + key injection
    permsvc/             permission service
    reviewer/            flagged-change reviewer worker
    correlator/          tool-call vs execution matcher
    policy-sign/         offline bundle signer
  pkg/
    vsockrpc/            gRPC over vsock / Hyper-V sockets
    bundle/              policy bundle schema, verify, apply
    evidence/            TPM evidence collection (go-attestation)
    sandbox/             runsc OCI bundle builder, netns, mounts
    hyperv/              Hyper-V control (WMI, PowerShell fallback)
    qemu/                QEMU, QMP, swtpm, virtiofsd control
    otel/                shared OpenTelemetry setup and event schema
  web/console/           admin web UI (React, TypeScript)
  plugins/spire-agentbox/  node attestor (agent + server)
  image/                 mkosi recipe, kernel config, systemd units, IMA policy
  sandbox-images/        base, codex, opencode, toolchain images
  configs/               Envoy, SPIRE, Keylime, Falco, OTel Collector, OPA rego
  deploy/
    compose/             single-server proof-of-concept stack
    helm/, argocd/       production control plane
    airgap/              offline install scripts
  packaging/             WiX (MSI), nFPM (deb, rpm)
  test/                  e2e (Codex, opencode, console), tamper matrix, performance
  docs/                  runbooks, threat model
```

## Appliance image

The appliance is stateless: every boot starts from a read-only, signed base disk, and only a separate encrypted data disk persists. Recycling the appliance is therefore just a reboot, and any in-memory tampering dies with it.

### Build

- **Recipe:** mkosi on an offline build host, using an internal Debian (or Fedora) mirror. The output is one disk image: an EFI system partition holding a unified kernel image (UKI), and a read-only root (erofs) with its dm-verity hash tree.
- **Kernel:** distribution kernel source with a reviewed config: lockdown LSM, forced module signatures, IMA, Hyper-V vsock (`CONFIG_HYPERV_VSOCKETS`) and virtio vsock, Hyper-V and virtio drivers only, no unused filesystems or drivers.
- **UKI contents:** kernel, initrd, and a fixed command line that includes the verity root hash, so changing any of them changes the measurement. Build it with `ukify`, which can [precompute the expected PCR 11 values](https://www.freedesktop.org/software/systemd/man/258/systemd-measure.html) for the image.
- **Packages in the root:** systemd, nftables, `runsc`, Envoy, SPIRE agent, Keylime agent (Rust), Falco (for gVisor monitoring), OpenTelemetry Collector (contrib build), `agentbox-manager`. Harnesses and toolchains are not in the base image; they ship as sandbox OCI images.
- **Flavors:** `prod` (no console, no debug) and `dev` (serial console, debug shell). Their measurements differ, and the production verifier never accepts `dev`.
- **Release manifest (signed):** UKI SHA-256, expected PCR 11 values per boot phase, verity root hash, the IMA allowlist of every executable in the root, and SBOM. The verifiers load it before the image is offered to any PC.

### Boot chain

1. **QEMU:** OVMF with the organization's Secure Boot keys enrolled in a vars template, then systemd-boot signed with the org key, then the UKI.
2. **Hyper-V:** Secure Boot with the Microsoft UEFI CA template, then a shim, then the UKI. Because client Hyper-V makes custom Secure Boot keys awkward, Secure Boot here is hardening only; the attestation of PCR 4 and 11 is the real check (see risks).
3. **systemd-stub** measures the UKI sections into PCR 11; the initrd opens the verity root using the hash from the measured command line; systemd-pcrphase marks each boot phase in PCR 11.

### Runtime hardening

| Setting | Effect |
| --- | --- |
| `lockdown=confidentiality`, `module.sig_enforce=1` | Even root cannot read or modify kernel memory or load unsigned modules |
| `kernel.yama.ptrace_scope=3`, set in early boot | No process can attach to another; cannot be lowered until reboot |
| IMA policy measuring every executable and executable mapping | Every program that runs is recorded and attested |
| Read-only verity root; `/etc` and `/var` volatile (tmpfs) | Nothing written at runtime survives a reboot except the data disk |
| No interactive login for people | Users are root only inside their sandboxes |
| Each service in its own user with systemd sandboxing (`ProtectSystem=strict`, `NoNewPrivileges`, minimal capabilities) | A bug in one service does not hand over the others |

### Data disk

A separate disk file holds sandbox workspaces, the OCI image cache and the log buffer. It is LUKS2-encrypted with a key sealed to the appliance's virtual TPM under a signed PCR 11 policy, so any image the organization signs can unlock it and nothing else can. A recovery key is escrowed to the control plane so a lost virtual TPM (for example after the PC is reimaged) costs a re-enrollment, not the workspaces.

## agentbox-manager

agentbox-manager is the only privileged process in the appliance that users interact with, through the host. It turns signed policy into sandboxes, networks and tunnels, and refuses anything the policy does not allow.

### Interfaces

| Interface | Peer | Purpose |
| --- | --- | --- |
| gRPC over vsock (port fixed in the image) | agentbox-host | Sandbox create, start, stop, list; open tunnels; status; apply bundle; recycle |
| SPIRE Workload API (local socket) | SPIRE agent | Its own 5-minute certificate for control-plane calls |
| HTTPS with mutual TLS | permsvc, registry, telemetry | Fetch bundles and images; send lifecycle events |
| Local socket | Falco gVisor collector | Sandbox trace configuration |

### Sandbox lifecycle

1. **Resolve the image.** Pull the requested sandbox image from the internal registry through Envoy, verify its signature against the organization's key with no transparency log, and cache it on the data disk.
2. **Build the root filesystem.** Unpack the image layers read-only and add a per-sandbox writable layer on the data disk.
3. **Write the OCI config.** User namespace, mounts allowed by policy (workspace read-write, caches read-only), CPU, memory and PID limits, environment (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, fixed local token), no extra capabilities.
4. **Start it.** `runsc --platform=systrap --network=sandbox --pod-init-config=<monitor config> run --detach --bundle <dir> <id>`, so gVisor connects to the trace collector before the workload starts.
5. **Report it.** Emit a lifecycle event (sandbox ID, user, image digest, policy version) for correlation.
6. **Stop or recycle.** Stopping kills the sandbox and keeps its writable layer; deleting removes both.

### Networking inside the appliance

- Each sandbox gets its own network namespace and a veth pair on the bridge `abx0`.
- nftables rules per sandbox allow only: the model endpoint (`10.88.0.1:4000`), the egress proxy (`10.88.0.1:3128`), the DNS responder (`10.88.0.1:53`) and, during a lease, its debug targets. Everything else is dropped and logged.
- The DNS responder is built into the manager and answers only for names in the sandbox's allowlist; everything else gets NXDOMAIN, which closes DNS as an exfiltration path.
- The appliance's own uplink accepts outbound traffic only from Envoy, SPIRE, Keylime, the OpenTelemetry Collector and the manager, to the control-plane addresses in the bundle.

### Access from the PC

- **Shell and IDE:** each sandbox image runs a small SSH server on its sandbox address. The manager joins a vsock stream from the host to that port, so `ssh` on the PC uses `ProxyCommand agentbox ssh-proxy <sandbox>` and VS Code Remote-SSH or JetBrains Gateway work unchanged. The authorized key is the user's per-device key.
- **Previews:** the same tunnel carries web previews, but through an HTTP-aware reverse proxy in the manager that blocks requests shaped like model API calls. Raw TCP tunnels need a flagged permission.
- **File sync:** on Linux PCs, virtio-fs shares mounted into the appliance and bound into the sandbox per policy. On Windows PCs, Mutagen over the SSH tunnel.

### Policy enforcement

Bundles are verified for signature, device ID, expiry and a version that only increases, then applied as a diff. If the current bundle expires or cannot be renewed, sandboxes keep running but lose the model endpoint and egress, so work is not lost and nothing new leaves.

### Recycle triggers

Idle for 30 minutes, nightly, end of an admin elevation window, a tamper signal, or a bundle change that requires it. Recycling stops sandboxes cleanly, reboots the appliance from the base disk and restarts sandboxes from their saved layers.

## Windows host layer

On Windows, `agentbox-host.exe` runs as a LocalSystem service. It owns the appliance VM, proves the PC's state, and is the only thing the CLI talks to. It holds no secrets that unlock model access.

### Prerequisites checked at install and every start

Windows 11 Pro or Enterprise 23H2 or later; TPM 2.0; Secure Boot on; virtualization-based security and memory integrity running with UEFI lock; Hyper-V enabled. Missing items are reported by `agentbox status` with the fix. Fleet or your existing tool sets the policies; the MSI only checks them.

### Appliance VM definition

Controlled through the Hyper-V WMI provider (`root\virtualization\v2`), with a PowerShell fallback for v1:

| Setting | Value | Reason |
| --- | --- | --- |
| Generation | 2 | UEFI, Secure Boot and virtual TPM |
| Secure Boot | On, Microsoft UEFI CA template | Hardening; attestation is the gate |
| Virtual TPM | `Set-VMKeyProtector -NewLocalKeyProtector`, then [`Enable-VMTPM`](https://learn.microsoft.com/powershell/module/hyper-v/enable-vmtpm) | Measured boot of the appliance |
| Checkpoints | Disabled, automatic checkpoints off | A checkpoint captures memory; re-enabling it is a tamper signal |
| Base disk | Signed VHDX, read-only file owned by SYSTEM, attached through a differencing disk that is discarded at every recycle | Stateless boot |
| Data disk | Per-user VHDX | Persistent workspaces |
| CPU and memory | Default 4 vCPU and 8 GB, static memory; adjustable by policy | Predictable performance |
| Network adapter 1 | Internal switch `agentbox-int` with a host NAT to the corporate network | Control-plane and registry traffic only |
| Network adapter 2 | Private switch `agentbox-dbg`, no NAT | Debug targets only |
| Integration services | Heartbeat and time sync on; guest file copy and key-value exchange off | Smallest host-guest surface |
| Stop action | Turn off | No saved memory state on disk |

The image is converted from raw to VHDX at build time, so the PC never runs conversion tools.

### Host-to-appliance channel

Hyper-V sockets: the service registers its service GUIDs under `GuestCommunicationServices` in the registry, using the VSOCK template GUID so each maps to a vsock port inside the Linux guest, as [Microsoft documents](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/make-integration-service). gRPC runs over these sockets. No IP path is needed for control.

### Host evidence

- At enrollment, create an attestation key in the TPM through go-attestation (Windows TPM Base Services and the Platform Crypto Provider) and register it with winattest.
- For each check, quote PCRs 0 to 7 and 11 to 14 over the server's nonce and attach the measured-boot log.
- Put a binding value in the quote's extra data: SHA-256 of the appliance's virtual TPM endorsement key, its attestation key and the VM's GUID. This ties the appliance to this physical TPM.

### Elevation watch (assurance level B)

Subscribe to Security log events 4732 and 4733 for the local Administrators group, and to Make Me Admin's events. When a user is added, tell the control plane to pause the appliance identity. When removed, recycle the appliance; Fleet then diffs services, drivers and scheduled tasks created during the window and flags anything new.

### Tamper sensors

Shipped as a Sysmon configuration and Wazuh rules: process access to `vmwp.exe` or `vmmem` with write rights (event 10), driver loads (event 6), debugger launches, and Hyper-V management events showing checkpoints, exports or configuration changes on the appliance VM. The service also hashes the VM's configuration at each start and reports changes.

## Linux host layer

On Linux, the same `agentbox-host` code runs as a systemd service and launches the appliance with QEMU/KVM. v1 supports Ubuntu 24.04 LTS and Fedora or RHEL 9 and later; others follow once their boot measurements are profiled.

### Appliance VM

QEMU runs as a transient systemd unit under a dedicated `agentbox-vm` user, with systemd sandboxing, launched with:

| Device | Configuration |
| --- | --- |
| Machine | `q35`, SMM on, KVM acceleration, `-cpu host` |
| Firmware | OVMF Secure Boot code (read-only) plus a per-user copy of an org vars template with the organization's PK, KEK and db enrolled |
| Virtual TPM | swtpm in socket mode, TPM 2.0, state directory readable only by root; attached as `tpm-crb` |
| Disks | Signed base image read-only with `snapshot=on`; per-user qcow2 data disk |
| Control channel | `vhost-vsock-pci` with a fixed guest CID per user |
| File sharing | One virtiofsd per allowed project root, sandboxed, plus a shared-memory backend |
| Network | passt (user-mode) for the uplink, restricted by host nftables rules matched on the QEMU unit's cgroup to control-plane and registry addresses |
| Debug targets | A separate bridge with tap devices and no forwarding to other interfaces |
| Console | None in `prod`; a QMP socket for the service only |

The host service only creates virtio-fs shares listed in the current signed bundle. It verifies the bundle signature itself rather than trusting the appliance.

### Host evidence

- **Keylime agent on the host** (physical TPM, push mode) attests the boot chain from the event log, plus IMA measurements of Agentbox's own files. To keep IMA quiet on a developer machine, those files are owned by a dedicated `agentbox-bin` user and the IMA policy measures only executables and executable mappings owned by it.
- **Binding**, as on Windows: agentbox-host takes a go-attestation quote whose extra data binds the appliance's virtual TPM keys and VM ID to the physical TPM.

### Strict mode (assurance level B)

An install-time option adds `lockdown=integrity sysctl.kernel.yama.ptrace_scope=3` to the measured kernel command line (PCR 8 with GRUB, PCR 12 with a UKI). The verifier checks for both. Users keep sudo for everything else. Turning strict mode off is just a reboot without the parameters, after which the verdict drops the PC to level C, with no change to their sandboxes.

## CLI, tray app and developer experience

Adoption by a skeptical R&D group depends on this layer more than any other. The bar: from install to a first working agent in under 10 minutes, and every failure explains itself.

### CLI

One binary, `agentbox`, identical on Windows and Linux, talking to agentbox-host over a named pipe or Unix socket restricted to the logged-in user.

| Command | Does |
| --- | --- |
| `enroll` | SSO device-flow sign-in, TPM registration, per-device SSH key creation |
| `up`, `down`, `recycle` | Start, stop or restart the appliance |
| `status` | Each layer's state (PC, appliance, identity, bundle, model access) with the fix for anything failing |
| `new <repo> [--tier N] [--image I]` | Create a sandbox from a repo and image |
| `list`, `stop`, `rm` | Manage sandboxes |
| `code <sandbox>`, `shell <sandbox>` | Open in VS Code over Remote-SSH, or a terminal |
| `ssh-proxy <sandbox>` | ProxyCommand used by SSH clients and IDEs |
| `preview <sandbox> <port>` | Forward a web preview to `localhost` on the PC |
| `sync add <path>` | Share a project folder (virtio-fs or Mutagen) |
| `allow <permission> --reason "..."`, `perms` | Request and list permissions |
| `key` | Show base URLs and the local token for tools inside a sandbox |
| `assist setup <editor>` | Point a host editor at the assist endpoint |
| `target lease`, `release`, `console`, `power`, `revert` | Debug target management |
| `level` | Show or change assurance level |
| `doctor` | Collect a redacted diagnostics bundle for support |

### Tray app

A status light (green, amber, red), quick actions (new sandbox, open last, pause), a permission request dialog, and notifications in plain language, such as "Model access paused: memory integrity is off. Turn it on in Windows Security and restart."

### IDE integration

`agentbox code` writes a managed SSH config include (`~/.ssh/agentbox_config`) with one host entry per sandbox and launches VS Code Remote-SSH on the sandbox's workspace. JetBrains Gateway uses the same entries. No IDE extension is needed for v1.

### Assist endpoint on the host

agentbox-host runs a small local proxy on `127.0.0.1:4001`. After host attestation, gatekeeper issues it a 15-minute assist token tied to the device; the proxy attaches it and the gateway rejects tool-calling fields. Editors are configured once with `agentbox assist setup`.

### Offline help

`agentbox help` and a bundled HTML guide cover every command and every `status` failure, so nobody needs an internet search to get unstuck.

## Attestation and identity

An appliance gets an identity only when three checks pass together: the PC's boot state, the appliance's boot and runtime state, and proof that this appliance runs on this PC. The SPIRE plugin is where the three meet.

### Enrollment

`agentbox enroll` creates a device record: device ID, user, OS, TPM endorsement key (and certificate if the chip holds one), host attestation key, Fleet inventory ID. Many firmware TPMs keep their endorsement certificates online with the vendor, which an offline network cannot fetch, so the plan uses trust on first use: enrollment is accepted only from a device already in Fleet's inventory, by its assigned user, on the corporate network. Where a certificate is on the chip, it is checked against manufacturer CA bundles stored offline.

### PC verdict

- **Windows (winattest):** verify the attestation key's link to the endorsement key (credential activation at enrollment), verify the quote over the server's nonce, replay the boot log against the PCRs, then parse it with go-attestation. Policy, written in Rego, checks properties rather than exact PCR values, so monthly Windows updates do not break it: Secure Boot on; boot, kernel and hypervisor debugging off; test signing off; virtualization-based security and memory integrity on.
- **Linux (Keylime):** measured-boot policy evaluated from the event log (Secure Boot state, approved boot loader and kernel signers, and the strict-mode command line for level B), plus the IMA runtime policy for Agentbox's own files.

### Appliance verdict

Keylime attests the appliance through its virtual TPM in push mode: expected PCR 4 and 11 values from the release manifest, and the IMA allowlist of every executable in the image. Virtual TPMs have no manufacturer certificate, so Keylime runs with [`require_ek_cert = false`](https://hectorproko.github.io/quartz/Security/Configuring-System-Integrity-Attestation-with-Keylime-on-RHEL-10) for appliances. Recent Keylime versions [removed the custom EK-check hook](https://issues.redhat.com/browse/RHEL-103012), so trust in the virtual TPM comes from the binding check below instead.

### Binding the appliance to the PC

The host's quote carries SHA-256 of the appliance's virtual TPM endorsement key, its attestation key and the VM ID as extra data. The plugin accepts an appliance only if those keys match what Keylime's registrar recorded for it, and the host quote is fresh and passing. A virtual TPM state copied to another PC fails this check.

### SPIRE node attestor plugin

1. The agent half, in the appliance, opens attestation with the server and receives a nonce.
2. It asks agentbox-host over vsock for a host quote over that nonce and the binding value, and adds the appliance's Keylime ID and keys.
3. The server half verifies the host quote (go-attestation), asks winattest or Keylime for the current PC verdict, asks Keylime for the appliance verdict, and checks the device is enrolled and not revoked.
4. On success it returns selectors for user, device, assurance level, tier cap and image version, and the SPIFFE ID `spiffe://agentbox.corp/appliance/<device>/<user>`.
5. The plugin supports re-attestation, so each renewal of the 15-minute appliance identity repeats steps 1 to 4.

### Workload identities and revocation

Registration entries give 5-minute certificates only to Envoy, the manager and the OpenTelemetry Collector in the appliance, matched by binary SHA-256, user and systemd unit. A revocation service subscribes to Keylime revocation events, winattest failures and tamper alerts, and evicts the agent from SPIRE so renewal fails. The SPIRE server runs at least two replicas on Postgres, signs with an intermediate from the internal CA, and keeps signing keys in the internal HSM or Vault.

## Model access path

A model request crosses two Envoys and one decision point. The LiteLLM key is attached only on the server, after the appliance's certificate has been checked, so there is nothing on a PC worth stealing.

### Local Envoy (in the appliance)

| Listener | Behavior |
| --- | --- |
| `10.88.0.1:4000` (model endpoint for sandboxes) | Accepts only the sandbox subnet. Strips any `Authorization` or `x-api-key` header from the sandbox. Adds `x-agentbox-sandbox` from the source address (mapping supplied by the manager) and a request ID. Per-sandbox rate limit. Forwards to the front gateway |
| `10.88.0.1:3128` (egress proxy) | HTTP CONNECT and plain HTTP with a dynamic forward proxy; destination allowlist from the bundle; everything else refused and logged |

Upstream connections use the appliance's 5-minute certificate, which the [SPIRE agent serves to Envoy over SDS](https://spiffe.io/docs/latest/microservices/envoy/), and verify the gateway's own SPIFFE ID. Access logs go to the appliance's OpenTelemetry Collector.

### Front gateway (control plane)

1. **Mutual TLS.** Envoy requires a client certificate from the `agentbox.corp` trust domain and passes the SPIFFE ID onward.
2. **Decision.** The `ext_authz` filter calls gatekeeper with the SPIFFE ID, headers and the parsed `model` field. Gatekeeper checks that the device is enrolled and not revoked, that the assurance level allows the tier, and that the model and budget fit. Decisions are cached per identity for 30 seconds to keep latency low.
3. **Key injection.** On allow, gatekeeper returns the header `Authorization: Bearer <LiteLLM key for this user, device and tier>`, which Envoy adds before routing to LiteLLM. Envoy's [credential injector](https://envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter) suits a fixed credential; the per-identity case needs the `ext_authz` header mutation.
4. **Metadata.** User, device, sandbox, tier and request ID go to LiteLLM as request metadata for logs and spend.

### Gatekeeper key management

Gatekeeper is the only holder of the LiteLLM master key. It keeps one virtual key per user, device and tier, created through LiteLLM's key API with allowed models, budgets, metadata and a 24-hour expiry, rotated daily and deleted on revocation. Keys live in gatekeeper memory and the LiteLLM database, never on a PC.

### Assist path

The host proxy calls a separate front-gateway listener with a 15-minute assist token signed by gatekeeper and bound to the device. Gatekeeper rejects any request body containing `tools`, `functions` or `tool_choice`, so this listener buffers request bodies (up to a configured limit). Rate limits are lower than for sandboxes.

### LiteLLM

Routes to the vLLM or SGLang backends by model name. A LiteLLM callback emits, for every completed response, the request ID, sandbox ID, model and the names and arguments of any tool calls the model made. The correlator uses these.

## Policy bundles, permission service and reviewer

Everything a sandbox may do is written in one signed bundle per device. The permission service is the only writer; the manager and host service are the only enforcers.

### Bundle format

```json
{
  "version": 4182,
  "device": "dev-7f3a", "user": "alice",
  "issued_at": "2026-11-10T09:00:00Z", "expires_at": "2026-11-11T09:00:00Z",
  "assurance_level": "B", "tier": 1,
  "resources": {"vcpu": 4, "memory_gb": 8, "disk_gb": 120, "max_sandboxes": 6},
  "images": ["registry.corp/agentbox/py-node:2026.11", "registry.corp/agentbox/c-cpp:2026.11"],
  "mounts": [{"host": "C:/src/payments", "mode": "rw", "flagged": false}],
  "egress": [{"host": "git.corp", "port": 443}, {"host": "nexus.corp", "port": 443}],
  "ports": {"preview": true, "raw_tcp": false},
  "models": ["deepseek-v3", "glm-4"],
  "debug_leases": [{"target": "win11-24h2-07", "ports": ["udp/50000"], "until": "2026-11-10T17:00:00Z"}],
  "recycle": {"idle_minutes": 30, "nightly": "02:00"},
  "endpoints": {"gateway": "gw.agentbox.corp:443", "telemetry": "tel.agentbox.corp:443"}
}
```

Signed with Ed25519 (minisign or keyed cosign; no transparency log). Two levels: an offline root key, held under a two-person rule, signs an online signing key that rotates monthly. The root public key is built into the appliance image and the installers. Verifiers reject bundles for another device, a lower version, or past expiry.

### Permission service

- **API:** `POST /requests` with permission and reason; returns the class (instant, flagged, approval, never) and expiry. Devices poll every 60 seconds and immediately after a CLI request, over mutual TLS.
- **Decisions:** OPA evaluates the request against a catalog in Git (developer hosts, project-root patterns, team quotas, image list) and user attributes from the identity provider (team, role, manager).
- **Approvals:** a small web UI for owners, with notifications through internal email or chat; approvals are time-boxed.
- **Expiry:** flagged grants last 14 days unless renewed with one command.
- **Audit:** every request, decision and change goes to an append-only table and the SIEM.

### Reviewer worker

1. Picks flagged items from a queue and assembles context: team and role, the user's recent repositories and sandbox images, what teammates hold, the stated reason.
2. Calls a model from a different family than the user's agents use, through LiteLLM with its own key, with a fixed prompt and a strict JSON output schema: `{"verdict": "consistent" | "unusual" | "escalate", "reason": "..."}`.
3. Anything that fails to parse counts as "escalate".
4. "Consistent" closes the item; 5% of those are sampled for a human. The rest land in the daily queue, where a person can keep, shorten or revoke.

The reviewer has no tools and no network path except LiteLLM, because it reads text the requester wrote.

## Debug targets subsystem

Debug targets reuse the host layer: lab servers run agentbox-host in lab mode, so local, lab and physical targets share one lease API and one cleanup path.

### Target catalog

Golden images built offline with Packer from ISOs in the internal artifact store: Windows client and server builds with test signing and KDNET prepared, and Linux distributions and kernels with debug symbols. Windows activation uses the internal KMS. Each image records which debugger and symbol set it needs.

### Target types

| Type | How it is created | Debug transport | Reset |
| --- | --- | --- | --- |
| Local VM | agentbox-host creates a sibling VM on the private `agentbox-dbg` switch. For Windows targets, Secure Boot is off in that VM so KDNET can be configured, as [Microsoft's KDNET guide notes](<https://msdn.microsoft.com/en-us/library/jj712701(v=vs.85).aspx>) | KDNET; QEMU gdbstub bound only to the debug bridge | Discard the differencing disk |
| Lab VM | agentbox-host in lab mode on a lab server | Same | Same |
| Physical rig | Lab inventory | KDNET on a dedicated NIC, serial through ser2net, kgdb, JTAG | PXE reimage (iPXE and dnsmasq); power cycle through the PDU |

### Lease flow

1. `agentbox target lease <class> --hours N`, or the agent's lab tool, calls the lease API in permsvc.
2. permsvc picks a target, updates the bundle's `debug_leases`, and for lab and physical targets asks the lab gateway to open rules from that appliance to that target only.
3. The manager opens per-sandbox nftables rules for the lease ports.
4. At expiry or release: rules close, the target resets, any debugger VM session is discarded, and crash dumps move to the lease's dump store.

### Debugger tooling for agents

- **Windows:** a small Windows Server Core debugger VM per lease, on the debug network, with Debugging Tools for Windows and [mcp-windbg](https://pypi.org/project/mcp-windbg) installed offline. The agent reaches it over MCP's HTTP transport. Symbols come from an internal symbol store filled through the model airlock for every Windows build in the catalog, because the public symbol server is unreachable.
- **Linux:** gdb with a GDB MCP server inside the sandbox, debug kernels from the catalog, and an internal debuginfod server.
- **Lab tools (MCP):** lease, release, revert, power-cycle, read serial console, fetch crash dumps.

### Dumps

Dumps from targets are free to use. Dumps from real machines go into a separate store and need Tier 2 approval, because they contain memory.

## Telemetry and detections

Everything is OpenTelemetry from the source onward, so the console, Grafana and later Splunk read the same records. Every record carries these resource attributes:

- `agentbox.user`
- `agentbox.device`
- `agentbox.sandbox`
- `agentbox.image`
- `agentbox.bundle_version`
- `agentbox.assurance_level`

Model calls also carry the GenAI attributes: model, input and output tokens, and finish reason.

### Sources

| Source | Collected where | What it gives |
| --- | --- | --- |
| gVisor runtime monitoring | Appliance, through Falco's gVisor source and an OTLP adapter | Every process start, network connect and open of a watched path inside each sandbox, without host eBPF |
| Codex built-in OTel export | Sandbox to appliance Collector | Prompts, tool calls with arguments, approval decisions, errors |
| opencode plugin | Sandbox to appliance Collector | A small plugin in the opencode image emits an OTel log for each tool call before and after it runs |
| Local and front Envoy access logs | Appliance and gateway | Every model request with identity, model, bytes and latency; every egress attempt with its verdict |
| LiteLLM spend logs | Control plane | Tokens and cost per request, tagged with the user and sandbox that gatekeeper passed as metadata |
| agentbox-manager | Appliance | Sandbox lifecycle, policy bundle applied, sync events, port previews |
| Host service | PC | Elevation, VM configuration hash, appliance restarts, Hyper-V or QEMU events |
| Sysmon and Wazuh (Windows), auditd (Linux) | PC to Wazuh, forwarded to the central Collector by its syslog receiver | Host-level tamper signals described in the host-layer sections |
| Verifiers and gatekeeper | Control plane | Attestation verdicts, revocations, denied requests |
| agentbox-console | Control plane | Every admin action, as an audit log record |

Gatekeeper adds a W3C `traceparent` to each model request. The appliance Collector links that request to the tool calls the harness reports, and to the process events that follow in the same sandbox, so one trace shows a full agent step.

### Correlator

The correlator runs every 30 seconds over ClickHouse. For each sandbox it takes the tool calls parsed from model responses (shell commands, file edits, fetches) and matches them to the gVisor events in the following window. It raises a finding in three cases:

- **Unexplained activity.** Execution or network activity has no matching tool call while an agent session is active.
- **Mismatched effects.** A tool call's effects differ from its text, such as a command that also connects to a host it never named.
- **Relay patterns.** Model-API-shaped traffic appears on preview ports or in synced files.

Its findings appear on the sandbox's activity timeline and as alerts.

### Detections

| ID | Detects | Action |
| --- | --- | --- |
| D1 | A process opening the Hyper-V worker or VM memory with write rights, or a debugger attached to it (Windows) | Revoke the device, recycle the appliance, page |
| D2 | ptrace, /proc/pid/mem or a debugger against QEMU or swtpm (Linux) | Revoke, recycle, page |
| D3 | Attestation failure, missed check-ins, or an appliance image not in the release manifest | Revoke automatically; ticket |
| D4 | A sandbox token used from anywhere but its own sandbox | Refuse, alert as a relay attempt |
| D5 | Model-API-shaped traffic through preview ports or file sync | Block the port, alert |
| D6 | Unexplained execution or network activity (from the correlator) | Alert; repeat offenders lose the flagged permission |
| D7 | A burst of denied egress attempts from one sandbox | Alert, attach the agent transcript |
| D8 | Mass delete, rename or overwrite in a mount | Pause the sandbox, alert the user and the on-call |
| D9 | Reads of credential files (SSH keys, tokens, browser profiles) in mounted folders | Alert, and suggest narrowing the mount |
| D10 | A flagged permission change the reviewer model judges out of character for the user | Alert to the approver queue |

### Retention and access

Full events stay in ClickHouse for 30 days, and metadata for one year. Prompt and response bodies are kept for 30 days and visible only to the investigator role. Grafana and the console read the same tables, so a five-person team works from one place. Only D1 to D4 and D8 page someone, and everything else lands in the daily queue.

## End-to-end flows

The flows below are the same in the proof of concept and in v1. Only the parts that check identity change: the console's light check becomes Keylime, winattest and SPIRE.

&#91;embedded content: first start · install to first model call, 11 steps\]

No key exists anywhere on the PC. The gateway attaches it only after it has checked the appliance's certificate, revocation state and budget.

### First start: from install to the first model call

1. The user installs the package and runs `agentbox enroll`, then signs in with the corporate account through the device-code flow.
2. The host service sends the device record and TPM public keys. The control plane accepts them only for a device in Fleet's inventory, assigned to that user, and issues a device certificate.
3. The host service starts the appliance. Its firmware and systemd-stub measure the image into the virtual TPM.
4. agentbox-manager asks the host service for a host quote that carries the appliance's virtual-TPM identity, then sends both quotes over the server's nonce.
5. On a passing verdict, the appliance receives its short-lived certificate and the signed policy bundle for its user and device.
6. `agentbox new --harness codex` makes a gVisor sandbox with the mounts, egress and models from the bundle, plus a token that only the local Envoy accepts from that sandbox.
7. Codex or opencode calls `llm.agentbox:4000`. The local Envoy swaps the sandbox token for the appliance's mutual TLS session to the front gateway.
8. Gatekeeper checks identity, revocation, assurance level, tier and budget, attaches the LiteLLM key and forwards the call. LiteLLM routes it to vLLM.
9. The response streams back to the harness. Every hop emits OpenTelemetry records stamped with the same trace.

### Permission change

1. The user runs `agentbox allow` or uses the tray.
2. permsvc classifies the change as automatic, needing approval, or allowed but flagged.
3. Automatic and flagged changes are signed into a new bundle at once and reach the appliance within seconds.
4. Flagged changes also go to the reviewer model and the console queue.
5. Changes needing approval wait in the console. The CLI shows who will decide and notifies the user of the outcome.

### Revocation and kill switches

- A failed check, a tamper detection or an admin's revoke adds the device to gatekeeper's deny list, which applies within 30 seconds. It also evicts the appliance's identity, so the certificate cannot be renewed.
- A kill switch blocks a model, a group or everything at gatekeeper. Sandboxes keep running; only model calls stop.

### Recycle and update

- At recycle (nightly, idle or on demand), the appliance reboots from its signed base. If a new release was staged, it boots that release and attests against the new manifest.
- Sandboxes are recreated from their images. Workspace files persist on the encrypted data disk or in synced folders.

### Admin action from the console

1. An admin's change is checked against their role and, for global changes, a second admin.
2. It is written to Postgres and turned into signed bundles or deny-list entries.
3. It is logged in the audit trail, which goes into the same OpenTelemetry stream as everything else.

## Offline operation

Nothing in build, install, enrollment or runtime may reach the internet (O1, O2). This section lists every outside dependency and its internal replacement.

### Mirrors and the import airlock

| Need | Internal service | Filled by |
| --- | --- | --- |
| Debian or Fedora packages for the image build and Linux hosts | aptly or a Pulp mirror | Scheduled snapshot through the airlock |
| Go modules | Athens proxy | Vendored per release; Athens for development |
| npm packages (console UI, opencode providers) | Verdaccio | Lockfile-driven import |
| PyPI packages for sandbox images | devpi | Lockfile-driven import |
| Container and OCI artifacts (sandbox images, appliance images, Helm charts) | Zot or Harbor | Built internally and signed with a keyed cosign |
| Codex CLI, opencode and other harness binaries | Artifact store | Imported by hash through the airlock, then smoke-tested |
| Windows ISOs, Debugging Tools, Sysmon, WiX | Artifact store | Manual import with hash check |

The airlock already exists for model weights in the security plan. Harnesses and packages use the same path: download outside, check signatures and hashes, scan, then import with a record of who approved it. Harness updates go through it on a fixed monthly cadence, so configuration-key changes are caught by the image smoke test before users see them.

### PKI and signing keys

- **Internal root CA.** It is kept offline, and it issues three intermediates:
  - device enrollment (step-ca in the proof of concept, Vault later);
  - SPIRE;
  - the TLS of control-plane services.
- **Release signing key.** It signs appliance images and release manifests and lives on an offline HSM. It is used in a signing ceremony with two of three key holders.
- **Policy root key.** It is offline and delegates to an online policy-signing key in Vault's transit engine for 30 days at a time. Per-device bundles are therefore signed automatically, while a stolen online key expires on its own.
- **Image signing key.** A keyed cosign key, not keyless, signs sandbox images. No transparency log is needed.

### Time

Certificates, quotes and bundle expiry all depend on time. The control plane uses the domain controllers, or a GPS-backed internal NTP server, as the source. Windows PCs already follow the domain time hierarchy. Inside the appliance, chrony syncs to the internal source and accepts Hyper-V time sync or kvm-clock only as a starting point. A clock that is off by more than five minutes is reported in attestation and shown in the console.

### TPM endorsement certificates

Many firmware TPMs do not carry their endorsement certificate on the chip, and the vendor download is unreachable offline. Enrollment therefore trusts on first use, gated by Fleet inventory and the corporate network, as described under attestation. Manufacturer CA bundles that do exist are imported through the airlock.

### Updates

Appliance images, host packages, sandbox images and policy all come from internal services, signed, as described in packaging. A PC that has been offline for weeks catches up on its next connection, and the verifier refuses images whose release has been revoked.

### Degraded mode (O3)

| What is down | What users see |
| --- | --- |
| Control plane unreachable | Sandboxes keep running with the last policy bundle. Model calls fail with a clear "model service unreachable" message. Non-model permissions stay valid for a 72-hour grace period after the bundle expires. |
| Telemetry tier unreachable | The appliance Collector buffers up to 2 GB on the data disk, then drops the lowest-priority events first; security events are kept last |
| LiteLLM or vLLM down | Gatekeeper returns a clear error; the harnesses show it |
| Console down | Enforcement continues. The console holds no runtime state that the gateway needs. |

## Control plane deployment

The control plane is everything server-side except the model servers: identity, attestation, policy, the gateway, the console and telemetry. In the local-first design it carries no sandboxes, so its size depends on the number of PCs and the volume of events, not on compute for agents.

### Proof of concept: one server

One Linux server runs everything under Podman Compose from `deploy/compose`. The planning size is 16 cores, 64 GB of RAM and 1 TB of SSD. The stack:

- Keycloak;
- step-ca;
- Postgres;
- ClickHouse;
- Grafana;
- the OpenTelemetry Collector;
- front Envoy and gatekeeper;
- permsvc and the reviewer;
- agentbox-console;
- LiteLLM, which can be the existing instance.

The internal DNS names (gw, tel, console and enroll under agentbox.corp) are the same as in production, so PCs need no reconfiguration when the control plane moves.

### Production: Kubernetes, offline

The production control plane is RKE2, installed from its air-gap bundle, on three control-plane nodes and a worker pool, with Argo CD syncing from the internal Git server.

| Service | Replicas | Notes |
| --- | --- | --- |
| Front Envoy and gatekeeper | 3 or more | Behind the existing load balancer; scaled on model-request rate |
| Keylime verifier and registrar | 2 or more | Verifiers are sharded by agent; Postgres backend |
| winattest | 2 or more | Stateless; evidence and verdicts in Postgres |
| SPIRE server | 2 | Shared Postgres datastore; signs with an intermediate from the internal CA |
| permsvc, reviewer workers, policy signer | 2 each | The reviewer calls a model from a different family through LiteLLM |
| agentbox-console | 2 | Stateless API; UI served as static files |
| Keycloak | 2 | Federated to Active Directory or LDAP |
| Postgres | 3 (CloudNativePG) | Point-in-time recovery to internal object storage |
| ClickHouse | 3 with ClickHouse Keeper | Replicated; daily backups |
| OpenTelemetry Collector gateway | 3 or more | Scaled on queue depth; mutual TLS from appliances |
| Grafana | 1 | Reads ClickHouse and Prometheus |
| Vault | 3 | Policy-signing transit key, LiteLLM master key, device CA |

### Sizing for a few thousand PCs

- **Attestation.** One check per PC every 2 to 5 minutes is about 10 to 25 quotes per second at 3,000 PCs. Each one is a few milliseconds of verification.
- **Telemetry.** Planning figure: up to 5,000 events per second at peak across the fleet. That is filtered at the appliance to process starts, connects, watched-path opens and tool calls. ClickHouse compresses it to tens of gigabytes per day. The proof of concept measures the real number.
- **Model gateway.** Gatekeeper's 30-second decision cache keeps its cost per request small. Latency target P3 applies.

### Phase 2: server-hosted sandboxes

Server-hosted sandboxes add a pool of KVM servers running agentbox-host in server mode. Each user gets their own appliance VM from the same image, with a virtual TPM bound to the server's TPM. They enroll as assurance level A and are reached over the same CLI with a remote flag. The control plane needs no change beyond a capacity scheduler. Their cost is server capacity per active user, which is why they come after the local rollout has shown real usage.

## Packaging, distribution and updates

### What ships

| Artifact | Format | Contents | Distributed by |
| --- | --- | --- | --- |
| Windows host package | MSI built with WiX, signed with the org code-signing certificate | Host service, CLI, tray app, Sysmon config, first appliance image | Microsoft Configuration Manager, Intune on-prem connector or GPO |
| Linux host package | deb and rpm built with nFPM, signed repository | Host service, CLI, tray app, QEMU, swtpm and virtiofsd dependencies, first appliance image | Internal apt and dnf repositories, rolled out by Ansible or Fleet |
| Appliance image | Signed disk image with release manifest, as an OCI artifact | UKI, verity root, manifest with expected measurements | Internal OCI registry, fetched by the host service |
| Sandbox images | OCI images signed with a keyed cosign | Base, Codex, opencode and toolchain images | Internal OCI registry, fetched by agentbox-manager |
| Control plane | Container images and Helm charts | All server components | Internal registry and Argo CD |

The Windows package checks for Hyper-V and offers to enable it. Enabling it needs one reboot, which the install guide schedules ahead. It also warns when VMware Workstation or VirtualBox runs in a mode that conflicts. The Linux package checks for KVM and the TPM device, and offers strict mode as an option.

### How an appliance update lands

1. Release engineering signs the image and manifest in the signing ceremony. The verifiers load the manifest before the image is offered to any PC.
2. The host service sees the new version in the policy bundle's release channel. It downloads the image to the inactive slot and checks the signature and hashes.
3. At the next recycle (nightly, idle or user-triggered), the appliance boots the new slot. Attestation passes only against the new manifest.
4. If boot or attestation fails twice, the host falls back to the previous slot and reports it. The console shows the version spread per channel.

### Release channels

| Channel | Who | Moves forward when |
| --- | --- | --- |
| Canary | Platform team and security | 48 hours with no new alerts or crash reports |
| Early | Volunteers, about 10% of R&D | One week, with error rates no worse than the current release |
| Broad | Everyone | Default |

The control plane supports the current and previous appliance release at once, so rollouts and rollbacks never need a flag day. Revoking a release adds it to a deny list that the verifiers check. PCs still on it drop to no model access until they update.

## Security of Agentbox itself

Agentbox becomes the gate in front of every model call, so it is a target in its own right: for the backdoored model, for curious users with admin rights, and for anyone who wants model access without controls.

### Threats and mitigations

| Threat | Mitigation |
| --- | --- |
| A user patches the appliance or its memory to get model access without controls | Measured boot and remote attestation; verity root; lockdown and IMA in the appliance; host tamper detections D1 and D2; credentials never on the PC |
| A user copies or relays the sandbox token | The token works only from its own sandbox through the local Envoy; D4 and D5 catch relays |
| Code in a sandbox attacks the appliance | gVisor; no identity material reachable from sandboxes (S5); manager API on vsock only, not on the sandbox network |
| A backdoored model steers the agent to exfiltrate | Egress policy, correlator findings, D6 to D9; no secrets in sandboxes by default |
| Compromise of the build or signing pipeline | Reproducible builds on an isolated builder, two-person review, release key on an offline HSM, SBOM per release |
| Stolen online policy key | 30-day delegation from the offline root; bundles expire in 24 hours; rotation runbook |
| Malicious or mistaken admin | Keycloak MFA, three roles, two-person rule for global kill switches and policy roots, full audit to ClickHouse and later Splunk |
| A harness (Codex, opencode) update adds unwanted behavior | Imported through the airlock by hash, pinned per image, egress-limited like any agent, monthly cadence |
| Compromise of the control plane | Hardened RKE2 nodes, network policies per service, Vault for secrets, no shell access for developers, separate admin network |

### Release process

- Every change is reviewed by a second engineer, and security-sensitive paths need a security reviewer: attestation, gatekeeper, policy signing, manager APIs and image recipe.
- Builds run on an isolated builder from the internal mirrors only. The image build is reproducible, so a second builder can confirm the hashes before signing.
- Each release carries an SBOM. The vulnerability scanner runs against the internal advisory mirror.
- An outside penetration test or internal red team covers the proof of concept before the PC pilot, and again before the broad rollout.

### Key custody

| Key | Where | Used by |
| --- | --- | --- |
| Internal root CA | Offline HSM in a safe | Ceremony only |
| Release signing | Offline HSM, two of three holders | Signing ceremony per release |
| Policy root | Offline HSM | Monthly delegation to the online key |
| Online policy signing | Vault transit, non-exportable | permsvc |
| LiteLLM master key | Vault | gatekeeper only |
| Device CA and SPIRE intermediates | Vault | step-ca or SPIRE |
| Image signing (cosign) | Vault transit | CI after review |

## Testing and validation

Every requirement ID maps to at least one automated test. The proof-of-concept suites (E2E-01 to E2E-10 and UI-01 to UI-12) become the base of the v1 suite and run nightly from week 5 onward.

### Test rigs

- **Windows.** Physical Windows 11 Pro and Enterprise runners with Hyper-V, two hardware models, on the nightly update ring and the production ring. Hyper-V with a virtual TPM is not reliable nested, so these are bare metal.
- **Linux.** Physical Ubuntu LTS and Fedora runners with KVM and a hardware TPM, one of them in strict mode.
- **Control plane.** The Compose stack for every merge, and a staging RKE2 cluster for nightly runs.
- **Model.** A small open model behind LiteLLM for nightly runs, and the production DeepSeek and GLM endpoints for weekly runs.

### Layers

| Layer | What runs | When |
| --- | --- | --- |
| Unit | Go tests, policy (Rego) tests, console UI component tests | Every commit |
| Integration | Compose stack with a QEMU appliance; enrollment, bundles, gateway, telemetry | Every merge |
| End to end | E2E and UI suites on the Windows and Linux rigs, Codex and opencode included | Nightly |
| Tamper matrix | The table below, on both rigs | Nightly for automated rows, per release for manual rows |
| Compatibility | Harness and toolchain matrix under gVisor | Per release and per harness update |
| Scale | A load generator simulating 5,000 appliances (attestation, bundles, telemetry, model calls) against staging | Per release |
| Resilience | Control plane down, telemetry down, network flaps, clock skew | Weekly |
| Red team | A deliberately backdoored test model that tries to exfiltrate on a trigger | Before the pilot and before broad rollout |

### Tamper matrix

| ID | Attempt | Expected result |
| --- | --- | --- |
| T1 | Modify a binary in the appliance root | Verity error; appliance does not boot or fails attestation |
| T2 | Boot a modified UKI or kernel command line | PCR 11 does not match the manifest; no identity |
| T3 | Boot the dev flavor | Refused by the production verifier |
| T4 | Attach a debugger to QEMU on a strict-mode Linux PC | Denied by ptrace\_scope=3; logged |
| T5 | Attach a debugger to QEMU on a non-strict Linux PC | Allowed; PC already at level C; D2 fires |
| T6 | Read or write the appliance memory from the Windows host (LiveCloudKd, MemProcFS) | D1 fires; device revoked within S2's limit |
| T7 | Replay an old quote | Nonce check fails |
| T8 | Copy the appliance VM to another PC | Host-quote binding fails; no identity |
| T9 | Use the sandbox token from the host or another PC | Refused; D4 |
| T10 | Run a model-API proxy on a preview port | Blocked; D5 |
| T11 | Roll back to a revoked image | Refused by the release deny list |
| T12 | Edit a policy bundle on disk | Signature check fails; last good bundle kept |

### Compatibility matrix

| Harnesses | Toolchains and workloads under gVisor |
| --- | --- |
| Codex CLI, opencode, Aider, VS Code agent extensions over Remote-SSH, JetBrains AI over Gateway | Go, Rust, C and C++ with CMake, Python with native wheels, Node, Java with Gradle and Maven, .NET on Linux, Git with large repositories, unit-test runners, headless browsers for front-end tests |

Workloads that fail under gVisor are listed in the docs with their workaround: a debug target VM, or the KVM platform where nested virtualization is available.

### Performance

P1 to P5 are measured on every nightly run and charted in Grafana. A regression of more than 10% blocks the release.

## Delivery plan

The plan is local first. The proof of concept puts the local sandbox, the console and the telemetry pipeline in users' hands by week 6. v1 then hardens identity and adds detections for a PC pilot in week 16. Server-hosted sandboxes start only once the local rollout is under way.

&#91;embedded content: delivery timeline · phases 0 to 2 by week\]

The dashed line marks the go or no-go at the end of the proof of concept. Everything after it builds on the same image and control plane.

### Phases and milestones

| Milestone | Week | Exit criteria |
| --- | --- | --- |
| M0: proof of concept accepted | 6 | Exit criteria of phase 0 met; go or no-go for v1 |
| M1: v1 feature complete | 14 | Keylime, winattest and SPIRE in place of light attestation; correlator and detections D1 to D10; offline signing; production control plane in staging |
| M2: PC pilot | 16 | Tamper matrix passes on both rigs; red-team exercise done; runbooks written; 50 to 100 pilot users on Windows and Linux |
| M3: organization-wide waves | 20 | Pilot targets met for four weeks; on-call rotation staffed; waves of about 500 users every two weeks |
| M4: phase 2 pilot | 28 | Server-hosted sandboxes for teams that asked, on the same image and control plane |
| M5: phase 2 general availability | 32 | Capacity plan approved from pilot usage |

### Workstreams and staffing

| Engineer | Phase 0 | Phase 1 | Waves and phase 2 |
| --- | --- | --- | --- |
| Linux platform | Appliance image, QEMU host layer, sandbox images | Strict mode, IMA, Keylime agents, debug targets for Linux | Server mode for KVM hosts |
| Windows platform | Hyper-V host layer, MSI, Windows CLI and tray | winattest, elevation watch, Sysmon rules, debug targets for Windows | Fleet rollout tooling |
| Identity and backend | Enrollment, gateway path, permsvc | SPIRE plugin, host-quote binding, offline signing, production control plane | Capacity scheduler for phase 2 |
| Console and telemetry | Console, OpenTelemetry pipeline, ClickHouse schema, E2E and UI suites | Correlator, detections, Grafana dashboards, scale tests | Splunk or other SIEM hand-off, operations handover |

Security review is part time throughout, with a full review before M2. The five-person operations team joins the pilot at M2, so they run it with the people who built it before the waves start.

## Operations runbooks

A five-person team runs Agentbox alongside the rest of the platform. Runbooks live in `docs/runbooks` and are linked from each alert in the console, so whoever is on call starts from the evidence and the steps at once.

| Runbook | Trigger | First steps |
| --- | --- | --- |
| Enrollment fails | User report or enroll error in the console | Check Fleet inventory match, TPM state, corporate network, Keycloak sign-in; re-run agentbox doctor |
| Attestation failure on one PC | D3 | Open the device's evidence in the console; compare with the manifest; if a known Windows update changed a property, update the Rego policy; otherwise treat as tamper |
| Attestation failures across many PCs | D3 burst | Check for a new Windows cumulative update or firmware rollout; pause revocation for that cause with a time-boxed exception; fix policy |
| Suspected tampering | D1, D2, T-matrix hits | Confirm revocation took effect; collect host logs through Wazuh; talk to the user; re-enroll only after review |
| Relay attempt | D4, D5 | Check the sandbox timeline; warn or suspend the user per policy |
| Destructive activity | D8 | Sandbox already paused; review the transcript with the user; restore from the PC's own backups or Git |
| Suspected model backdoor trigger | D6, D7 clusters across users on one model | Kill switch for that model; preserve transcripts; notify the model-intake owner; compare with other model families |
| Appliance release rollout | Release approved | Promote canary to early to broad in the console; watch version spread and error rates; roll back by channel |
| Control plane outage | Gateway or verifier down | Users keep their sandboxes; restore the service; verify appliances re-attest within 10 minutes |
| Telemetry backlog | Collector queue depth alert | Scale the gateway tier; check ClickHouse merges; appliances buffer locally meanwhile |
| Flagged-change queue | Daily | Work the reviewer's flagged items oldest first; turn repeated safe patterns into automatic rules |
| Add Splunk or another SIEM | Project request | Enable the splunk\_hec or otlphttp exporter in console settings, test-send, then add it to the pipelines in Git |
| Key rotation | Calendar or suspected exposure | Follow the ceremony for the key; publish new trust bundles; watch for verification errors |

On-call is one person at a time, weekly. Only D1 to D4, D8 and service outages page outside working hours.

## Risks and open questions

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| Hyper-V cannot be enabled on part of the Windows fleet (conflicts with VMware or VirtualBox, older hardware, VBS policy) | Medium | High | Inventory in week 1 of the proof of concept; VMware Workstation and VirtualBox run on the Hyper-V platform in current versions; server-hosted sandboxes in phase 2 for the rest |
| Toolchains or tests misbehave under gVisor | Medium | Medium | Compatibility matrix from the proof of concept; debug target VMs for heavy work; KVM platform where nesting works |
| R&D rejects the sandbox as too slow or too limiting | Medium | High | Volunteer week in the proof of concept; P1 to P5 targets; self-service permissions; under-10-minute setup |
| Codex or opencode configuration changes break the images | High | Low | Version pinning, monthly cadence, smoke test in the image build |
| LiteLLM's Responses API bridge or tool-calling for DeepSeek and GLM behaves differently from OpenAI models | Medium | Medium | Test in week 3; fall back to the chat wire API in Codex where the pinned version still supports it |
| Windows host admins can read guest memory | Certain | Medium | Accepted residual risk, covered by detections, recycling and keeping credentials off PCs, as in the security plan |
| Telemetry volume is higher than planned | Medium | Medium | Filter at the appliance; measure in the proof of concept; size ClickHouse from real numbers |
| The single-server proof-of-concept control plane fails during the volunteer week | Low | Low | Nightly backups; users keep working without model access |
| Attestation false positives after Windows updates lock users out | Medium | High | Property-based Rego policy; pilot ring tracks updates first; time-boxed exceptions runbook |

### Open questions

- Which Windows editions and hardware models are in the fleet, and how many PCs already run VBS and memory integrity?
- Which identity source does Keycloak federate to? Do users need MFA at enrollment?
- Will the security team accept prompt and response bodies in ClickHouse, with 30-day retention for the investigator role?
- Is there a Splunk instance to target now, and which index and HEC token policy should it use?
- Should users be able to bring their own harness beyond Codex and opencode? If so, how is it imported and pinned?
- Which teams want server-hosted sandboxes in phase 2, and what hardware is available for them?

## Sources

- [Codex CLI configuration, model providers, sandbox and OTel settings](https://github.com/openai/codex/blob/main/docs/config.md)
- [Codex documentation](https://developers.openai.com/codex/)
- [opencode providers (OpenAI-compatible provider)](https://opencode.ai/docs/providers/)
- [opencode configuration](https://opencode.ai/docs/config/)
- [opencode plugins](https://opencode.ai/docs/plugins/)
- [LiteLLM Responses API](https://docs.litellm.ai/docs/response_api)
- [OpenTelemetry Collector Splunk HEC exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/splunkhecexporter)
- [OpenTelemetry Collector ClickHouse exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/clickhouseexporter)
- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
- [gVisor runtime monitoring](https://gvisor.dev/docs/user_guide/runtimemonitor/)
- [Keylime](https://keylime.dev/)
- [SPIRE](https://spiffe.io/docs/latest/spire-about/)
- [go-attestation](https://github.com/google/go-attestation)
- [mkosi](https://github.com/systemd/mkosi)
- [ukify](https://www.freedesktop.org/software/systemd/man/latest/ukify.html)
- [Keycloak](https://www.keycloak.org/documentation)
- [step-ca](https://smallstep.com/docs/step-ca/)
- [RKE2 air-gap install](https://docs.rke2.io/install/airgap)
- [Playwright](https://playwright.dev/)

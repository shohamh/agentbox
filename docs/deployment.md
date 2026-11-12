# Deploying Agentbox on the company network

For the people who will own the deployment. It says plainly what is ready, what
is still a test-bed shortcut you must replace first, and the order to do things in.
Companion guides: [litellm-admin.md](litellm-admin.md) for whoever runs LiteLLM,
[user-guide.md](user-guide.md) for developers, [hyperv.md](hyperv.md) for the PC side.

## What you are deploying

| Piece | Where | Runs as |
|---|---|---|
| Control plane: Keycloak (identity), step-ca (device CA), Postgres, ClickHouse, gatekeeper, permission service, reviewer, correlator, console, telemetry gateway, registry, front Envoy | one or more Linux servers | Docker Compose (`deploy/compose`) |
| Your LiteLLM | where it is today | unchanged ([litellm-admin.md](litellm-admin.md)) |
| Appliance: manager, gVisor, local Envoy, telemetry collector, vTPM | every PC | a Hyper-V Generation 2 VM (Secure Boot, vTPM) built by `scripts/build-vm.sh` |
| Host service and CLI (`agentbox-host`, `agentbox`) | every PC | a Windows service and a command-line tool |
| Sandbox images: `base`, `codex`, `opencode`, `claude-code`, `omp` | registry, pulled by appliances | built by `scripts/publish-images.sh` |

## What is ready, and what is not

**Verified end to end** (container test bed: 26 automated tests; Hyper-V on a real
Windows 11 PC: install, attestation, a sandbox, and the E2E set in `docs/hyperv.md`):
enrollment through OIDC; attestation of the appliance (virtual-TPM quote against a signed release
manifest); signed per-device policy; short-lived LiteLLM keys per attested sandbox,
revoked within seconds; kill switches; budgets; egress control; telemetry into the
console; Claude Code, Codex, opencode and omp running real tool loops; SSH and
VS Code access.

**Test-bed shortcuts. Replace these before real users:**

| # | Shortcut today | What to do instead | Effort |
|---|---|---|---|
| 1 | Identity is a demo Keycloak realm: users `bob`, `alice`… with password `Passw0rd!`, plain HTTP, `allow_password_grant: true` in the PC's `host.json` | Federate Keycloak with your IdP (identity brokering, LDAP or SAML), delete the demo users, set your own client secret, put Keycloak and the console behind TLS, and turn password grant off. Users sign in with the device-code flow on a real identity. | days |
| 2 | Names: the services are `*.agentbox.corp` and the names are built into images and configs | Create an internal DNS zone `agentbox.corp` (private, not delegated) pointing `kc`, `console`, `enroll`, `ca`, `gw`, `permsvc`, `tel` and `registry` at the control plane (PCs and appliances resolve them). No code change. | hours |
| 3 | Secrets live in a Docker volume (`agentbox_secrets`, files mode 0644) and `deploy/compose/.secrets/compose.env` | Keep the release-signing key offline or in an HSM and sign manifests there (`docs/runbooks/key-rotation.md`); give the services their secrets from your secret store as files (the LiteLLM master key especially). Restrict the Docker host. | days |
| 4 | step-ca runs with a root generated at first start | Issue its intermediate from your PKI, or keep the generated root offline and back it up: every appliance trusts this root | day |
| 5 | One Docker Compose host; no backup, no failover | Run on a dedicated VM, back up the Postgres volumes (`agentbox`, `litellm`) and ClickHouse, and monitor it. If the control plane is down, running sandboxes keep working but nothing new starts and model calls fail (E2E-11..15 cover this). A multi-node deployment is not built. | days |
| 6 | The PC installer is `scripts\install-hyperv.ps1`, a script that copies from `dist\`, runs `docker compose` and reads the root CA from a Docker volume | Build a signed installer (MSI) that carries the host service, the CLI, the appliance disk (about 1 GB), the control-plane root CA and a `host.json` with your `enroll_url` and `oidc_issuer`; deploy it with Intune or GPO. Not built. | weeks |
| 7 | Windows with Hyper-V is the only verified PC path. A Linux (KVM) driver exists and boots under QEMU in tests but has not run on a real PC | Pilot on Windows PCs with Hyper-V enabled (Pro/Enterprise) | |
| 8 | Attestation is "light": the appliance's vTPM signs a measurement of the appliance image, checked against your signed release. It proves the appliance is the release you published, not that the PC around it is untampered | Accept for the pilot. Host attestation (Keylime, winattest) is not built; assurance level stays C, so tiers above 2 are not available | |
| 9 | Sandbox images are built on the build host and pushed to the bundled registry unsigned (digests are pinned in the policy) | Build them in CI, push to your registry, sign (cosign), pin digests | days |
| 10 | Scale: tested with a handful of sandboxes and two PCs | Pilot with 5 to 10 users first. Budget 3 to 4 GB of RAM per PC for the appliance VM (`memory_mb` in `host.json`) | |

## Order of work

1. **A control-plane host.** A Linux VM with Docker and Compose, 8 GB RAM and 50 GB
   disk to start; DNS names (shortcut 2); TLS in front of Keycloak and the console (1).
2. **Build and bring it up** from a checkout of this repository, with your LiteLLM:

   ```bash
   scripts/build-bins.sh
   scripts/bootstrap.sh                          # signing keys, secrets volume, compose.env
   # put your LiteLLM master key where the gatekeeper reads it (never on a command line):
   printf '%s' "$LITELLM_MASTER_KEY" | docker run --rm -i -v agentbox_secrets:/s debian:bookworm-slim sh -c 'cat > /s/litellm_master_key'
   SKIP_BINS=1 scripts/release.sh                # appliance image and the signed release manifest
   export LITELLM_URL=https://litellm.corp.example  REVIEWER_MODEL=small   # see litellm-admin.md for the rest
   docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/external-litellm.yml \
     --env-file deploy/compose/.secrets/compose.env up -d --build --wait
   scripts/publish-images.sh                     # sandbox images to the registry; then restart the console
   ```

   Run `scripts/preflight.sh` first ([litellm-admin.md](litellm-admin.md), section 4).
3. **Edit the policy.** The first start seeds groups, users and the egress catalog
   from `configs/control/console/seed.json`: set your group's `models` (names from
   your LiteLLM), `images`, `egress` hosts and user budgets, **before** the first start
   of the console (the seed is read once: afterwards edit in the console, Policy).
   Replace the demo inventory and users.
4. **Build the PC side**: `scripts/build-windows.sh` (CLI and host service) and
   `scripts/build-vm.sh` (appliance disk and manifest). Pilot PCs: `scripts\install-hyperv.ps1`
   as in [hyperv.md](hyperv.md), after pointing `host.json` at your names (until shortcut 6 is done).
5. **Pilot.** Five to ten developers. Each runs `agentbox enroll`, then
   [user-guide.md](user-guide.md). Watch the console's Overview and Alerts, and the
   gateway denials ([litellm-admin.md](litellm-admin.md), section 5).
6. **Acceptance on your network** (the automated suite drives Docker, so these are manual there):
   `agentbox status` all ok on a pilot PC; `scripts/preflight.sh` all ok; a Claude Code
   and a Codex task finish; the console shows the `model.call` events with the user,
   device and sandbox; revoke the PC in the console and see the model calls stop
   within seconds; stop LiteLLM and see the clear "model service unreachable" message.

## Agents and tools

| Tool | Status | How it is wired |
|---|---|---|
| Claude Code | tested (E2E-16, including the interactive start) | image `claude-code`; Anthropic Messages API through LiteLLM `/v1/messages`; model roles from the policy; its own telemetry goes to the console |
| Codex | tested (E2E-03, 06, 07) | image `codex`; Responses API through LiteLLM |
| opencode | tested (E2E-04) | image `opencode`; chat completions |
| omp (Oh My Pi) | tested (E2E-17) | image `omp`; chat completions; Bun and omp pinned in the image |
| Orca | by design over SSH into a sandbox; Orca itself is not tested | [user-guide.md](user-guide.md) |
| Anything else that speaks OpenAI or Anthropic APIs | works in the `base` image with the preset environment | [user-guide.md](user-guide.md) |

To add an agent for everyone: copy `sandbox-images/claude-code` or `omp`, install the
agent pinned, write its config so that it uses `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL`
and `$AGENTBOX_MODEL` (the sandbox sets them from the policy), add the name to
`scripts/publish-images.sh` and to the groups' `images`, and add a test next to
E2E-16. The test is a real task run through `agentTask`.

## Operating it

- Runbooks for the situations that matter (attestation failures, outages, relay attempts, destructive activity…): `docs/runbooks/`.
- Keys and rotation: `docs/runbooks/key-rotation.md`. Model keys rotate by themselves.
- Retention and export (Splunk): `docs/runbooks/add-splunk.md`, console Settings.
- Updating the appliance: build a new release, publish it, roll it by channel
  (`docs/runbooks/release-rollout.md`).
- Tests you can run after any change: `scripts/test.sh` (see [testing.md](testing.md)).

# Hyper-V install on a Windows PC

This is the production shape of the PC side: the host service runs as a Windows
service (LocalSystem), owns a Hyper-V Generation 2 appliance VM, and talks to the
appliance over Hyper-V sockets. For the test, the control plane runs in Docker
Desktop on the same PC.

## What you get

| Piece | How |
|---|---|
| Appliance image | `image/vm/Dockerfile.vm`: Debian signed shim, signed GRUB and signed kernel (Secure Boot with the Microsoft UEFI CA template), systemd, our appliance binaries, minimal initramfs |
| VM | Generation 2, Secure Boot on, virtual TPM, checkpoints off, SCSI disks, Default Switch (NAT), integration services: heartbeat and time sync only |
| Disks | signed base VHDX (never written) + a differencing disk discarded at every VM start + one dynamic data VHDX |
| Measured boot | `agentbox-measure boot` extends the hash of every measured file into PCR 11 of the vTPM (`/dev/tpmrm0`); the signed release manifest carries the expected value |
| Control channel | Hyper-V sockets, service GUID for vsock port 7000 (registered by the service) |
| Uplink to the control plane | `agentbox-uplink` in the VM listens on 127.0.0.2 for ports 443, 8443, 4318, 5000 and 18443 and carries each connection over Hyper-V sockets (same port number) to the host service, which relays it to 127.0.0.1 |
| Host service | `agentbox-host.exe` as the `agentbox-host` Windows service; the CLI talks to it over the named pipe `\\.\pipe\agentbox-host` |

## Build

```bash
bash scripts/build-windows.sh                 # dist/windows/*.exe
bash scripts/build-vm.sh                      # dist/vm/agentbox-appliance.vhdx; signs the manifest and puts it where the console reads it
TEST=1 bash scripts/build-vm.sh && bash scripts/test-vm-qemu.sh   # boot test under QEMU (Secure Boot, software TPM)
```

`FLAVOR=dev bash scripts/build-vm.sh` builds a dev image with a serial-console
login for debugging; its measurement differs and the production verifier never
accepts it.

## Install (elevated PowerShell)

Needs Hyper-V enabled (Windows Features, restart) and the control plane up
(`bash scripts/up.sh`).

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install-hyperv.ps1
# then, from a normal shell:
"Passw0rd!" | & "C:\Program Files\Agentbox\bin\agentbox.exe" enroll --user alice --password-stdin
& "C:\Program Files\Agentbox\bin\agentbox.exe" status
& "C:\Program Files\Agentbox\bin\agentbox.exe" doctor      # boot evidence, TPM, network, journal tail
```

The installer publishes the control-plane ports on loopback only
(`deploy/compose/hyperv.yml`). The appliance reaches them through the uplink: at
boot it points the `*.agentbox.corp` names at 127.0.0.2, where `agentbox-uplink`
forwards each connection over Hyper-V sockets to the host service. Nothing listens
on the virtual switch, no Windows Firewall rule is needed, and a changed Default
Switch address after a Windows restart does not matter. TLS names and ports are
the same as in a datacenter install.

`agentbox doctor` includes a TPM probe: each TPM operation the appliance needs
(PCR read, attestation key, storage and endorsement keys), tried on its own and
timed, so a TPM that behaves differently is diagnosed in one run.

`scripts\install-hyperv.ps1 -Uninstall` removes the service, the VM and the files.

## Run the tests against it

Same E2E suite as the container bed (see `docs/testing.md`), with the Windows PC as
`pc-win`. Publish the sandbox images (`bash scripts/publish-images.sh`, then restart
the console), start the Git stand-in and publish ClickHouse for the test runner:

```bash
docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/hyperv.yml \
  -f deploy/compose/appliance.yml -f deploy/compose/windows.yml \
  --env-file deploy/compose/.secrets/compose.env up -d --wait corp-services clickhouse
```

```powershell
$env:AGENTBOX_EXE = "C:\Program Files\Agentbox\bin\agentbox.exe"
$env:E2E_PC1 = "pc-win"; $env:E2E_APP1 = "appliance-hyperv"; $env:STRICT_MESSAGES = "1"
$env:E2E_HOSTMAP = "kc.agentbox.corp:8080=127.0.0.1:18180,console.agentbox.corp:18080=127.0.0.1:18080,clickhouse:8123=127.0.0.1:18123"
dist\windows\e2e.exe -test.v -test.run "^TestE2E_0[1345678]_|^TestE2E_1[1-5]_|^TestT10_"
```

A Hyper-V appliance has no container, so the steps that exec into it are skipped
(E2E-05's direct-LiteLLM baseline for P3, E2E-06's appliance scan and replay from
the appliance) and the tests built on it do not apply (E2E-09 cut, T9, T12).
E2E-02 and E2E-10 need the simulated container PCs.

## Verified on Hyper-V (Windows 11 Pro, 2026-11-12)

Installed with `scripts\install-hyperv.ps1`: Generation 2 VM with Secure Boot
(Microsoft UEFI CA) and a virtual TPM, the `agentbox-host` service, the uplink.
The TPM probe passes every operation on the Hyper-V vTPM (manufacturer MSFT),
the PC enrolls, attestation passes against the signed release manifest, the
policy bundle arrives, and a sandbox runs under gVisor. The E2E selection above
passes: E2E-01, 03, 04, 05, 06, 07, 08, 11 to 15 and T10 (14 tests).

The image also boots under QEMU with Secure Boot and a software TPM
(`scripts/test-vm-qemu.sh`).

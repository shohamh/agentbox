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

The installer publishes the control plane on this PC's Default Switch address
(`deploy/compose/hyperv.yml`), because the VM reaches the host there; at boot the
appliance points the `*.agentbox.corp` names at its default gateway. That address
can change after a Windows restart: rerun the installer if `agentbox doctor` shows
the appliance cannot reach the control plane.

`scripts\install-hyperv.ps1 -Uninstall` removes the service, the VM and the files.

## Run the tests against it

Same E2E suite as the container bed (see `docs/testing.md`), with the Windows PC as
`pc-win` and `E2E_APP1` naming a service that can read the appliance. A Hyper-V
appliance has no container, so tests that exec into it (E2E-09 cut, T12 bundle
edit) do not apply; the rest run unchanged.

## What is not verified

The image boots under QEMU with Secure Boot and a software TPM in CI-style
tests. Booting on Hyper-V itself needs an elevated session on a machine with
Hyper-V, which the build environment did not have.

//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows/registry"

	"agentbox/pkg/vsockrpc"
)

// hypervDriver defines and runs the appliance as a Hyper-V Generation 2 VM
// (Secure Boot with the Microsoft UEFI CA template, virtual TPM, checkpoints
// off, read-only signed base VHDX through a differencing disk discarded at
// every recycle) using the PowerShell fallback the plan allows for v1.
// Not exercised in the container test bed.
type hypervDriver struct {
	cfg  Config
	vmID string
}

const vmName = "agentbox-appliance"

func newHyperV(cfg Config) (Driver, error) { return &hypervDriver{cfg: cfg}, nil }

func ps(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (h *hypervDriver) Name() string { return "hyperv" }

func (h *hypervDriver) ChannelAddr() string {
	if h.vmID == "" {
		id, _ := ps(context.Background(), "(Get-VM -Name "+vmName+").Id.Guid")
		h.vmID = id
	}
	return "hvsock://" + h.vmID + ":7000"
}

func (h *hypervDriver) Checks() []Check {
	ctx := context.Background()
	q := func(name, script, want, fix string) Check {
		out, err := ps(ctx, script)
		return Check{Name: name, OK: err == nil && strings.EqualFold(out, want), Info: out, Fix: fix}
	}
	return []Check{
		q("Hyper-V", "(Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V).State", "Enabled", "Enable Hyper-V in Windows Features and restart"),
		q("TPM 2.0", "(Get-Tpm).TpmReady", "True", "Turn on the TPM in firmware settings"),
		q("Secure Boot", "Confirm-SecureBootUEFI", "True", "Turn on Secure Boot in firmware settings"),
		q("Memory integrity", "(Get-CimInstance -Namespace root/Microsoft/Windows/DeviceGuard -ClassName Win32_DeviceGuard).SecurityServicesRunning -contains 2", "True",
			"Turn on Memory integrity in Windows Security > Device security > Core isolation, then restart"),
	}
}

// registerVsock maps vsock port 7000 to a Hyper-V socket service GUID.
func registerVsock() error {
	g := vsockrpc.VsockServiceID(7000)
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\`+g.String(), registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("ElementName", "Agentbox control channel")
}

func (h *hypervDriver) Start(ctx context.Context) error {
	if err := registerVsock(); err != nil {
		return fmt.Errorf("register hvsock service: %w", err)
	}
	st := h.cfg.StateDir
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
if (-not (Get-VM -Name %[1]s -ErrorAction SilentlyContinue)) {
  New-VHD -Path '%[2]s\diff.vhdx' -ParentPath '%[3]s' -Differencing | Out-Null
  New-VM -Name %[1]s -Generation 2 -MemoryStartupBytes 8GB -VHDPath '%[2]s\diff.vhdx' -SwitchName agentbox-int | Out-Null
  Add-VMHardDiskDrive -VMName %[1]s -Path '%[2]s\data.vhdx'
  Set-VM -Name %[1]s -ProcessorCount 4 -StaticMemory -CheckpointType Disabled -AutomaticCheckpointsEnabled $false -AutomaticStopAction TurnOff
  Set-VMFirmware -VMName %[1]s -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority
  Set-VMKeyProtector -VMName %[1]s -NewLocalKeyProtector
  Enable-VMTPM -VMName %[1]s
  Disable-VMIntegrationService -VMName %[1]s -Name 'Guest Service Interface','Key-Value Pair Exchange'
  Add-VMNetworkAdapter -VMName %[1]s -SwitchName agentbox-dbg
}
Start-VM -Name %[1]s`, vmName, st, h.cfg.BaseImage)
	if out, err := ps(ctx, script); err != nil {
		return fmt.Errorf("start VM: %v: %s", err, out)
	}
	return nil
}

func (h *hypervDriver) Stop(ctx context.Context) error {
	// Turn off (no saved memory state on disk), then discard the differencing disk.
	script := fmt.Sprintf(`Stop-VM -Name %s -TurnOff -Force; Remove-Item '%s\diff.vhdx' -ErrorAction SilentlyContinue`, vmName, h.cfg.StateDir)
	_, err := ps(ctx, script)
	return err
}

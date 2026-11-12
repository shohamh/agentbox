//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"

	"agentbox/pkg/vsockrpc"
)

// hypervDriver defines and runs the appliance as a Hyper-V Generation 2 VM,
// driven through PowerShell (the plan's v1 fallback to the WMI provider). It
// must run as LocalSystem or an administrator.
//
//	Secure Boot   on, Microsoft UEFI CA template (Debian's signed shim and kernel boot)
//	TPM           virtual TPM with a local key protector: measured boot of the appliance
//	Checkpoints   disabled (a checkpoint captures memory)
//	Base disk     the signed VHDX, never written; the VM boots a differencing disk that
//	              is discarded at every start, so every boot is from the signed image
//	Data disk     one dynamic VHDX per PC: workspaces, image cache, collector queue
//	Network       the Default Switch (NAT): the appliance reaches the control plane
//	Integration   heartbeat and time sync on; guest file copy and key-value exchange off
//	Channel       Hyper-V sockets, service GUID for port 7000
type hypervDriver struct {
	cfg Config

	mu   sync.Mutex
	vmID string
}

const (
	vmName      = "agentbox-appliance"
	vsockPort   = 7000
	psTimeout   = 5 * time.Minute
	defaultSwch = "Default Switch"
)

func newHyperV(cfg Config) (Driver, error) {
	if cfg.SwitchName == "" {
		cfg.SwitchName = defaultSwch
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = 4096
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = 4
	}
	if cfg.DataGB == 0 {
		cfg.DataGB = 20
	}
	return &hypervDriver{cfg: cfg}, nil
}

func ps(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (h *hypervDriver) Name() string { return "hyperv" }

func (h *hypervDriver) vmGUID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vmID == "" {
		id, err := ps(context.Background(), "(Get-VM -Name "+vmName+" -ErrorAction Stop).Id.Guid")
		if err == nil && len(id) == 36 {
			h.vmID = id
		}
	}
	return h.vmID
}

func (h *hypervDriver) ChannelAddr() string {
	// The VM ID changes if the VM is recreated: look it up until it is known.
	id := h.vmGUID()
	if id == "" {
		id = "00000000-0000-0000-0000-000000000000"
	}
	return fmt.Sprintf("hvsock://%s:%d", id, vsockPort)
}

func (h *hypervDriver) Checks() []Check {
	ctx := context.Background()
	q := func(name, script, want, fix string) Check {
		out, err := ps(ctx, script)
		return Check{Name: name, OK: err == nil && strings.EqualFold(out, want), Info: out, Fix: fix}
	}
	cs := []Check{
		q("Hyper-V", "(Get-Service vmms).Status", "Running", "Enable Hyper-V in Windows Features and restart"),
		q("virtual machine", "(Get-VM -Name "+vmName+" -ErrorAction SilentlyContinue).State", "Running", "restart the agentbox-host service: it creates and starts the appliance VM"),
		q("Secure Boot (VM)", "(Get-VMFirmware -VMName "+vmName+" -ErrorAction SilentlyContinue).SecureBoot", "On", "recreate the VM: Remove-VM "+vmName+" -Force, then restart the service"),
		q("virtual TPM", "(Get-VMSecurity -VMName "+vmName+" -ErrorAction SilentlyContinue).TpmEnabled", "True", "recreate the VM"),
		q("switch", "(Get-VMSwitch -Name '"+h.cfg.SwitchName+"' -ErrorAction SilentlyContinue).Name", h.cfg.SwitchName, "the Default Switch is created by Hyper-V; restart the Hyper-V Virtual Machine Management service"),
		q("memory integrity", "(Get-CimInstance -Namespace root/Microsoft/Windows/DeviceGuard -ClassName Win32_DeviceGuard).SecurityServicesRunning -contains 2", "True",
			"Turn on Memory integrity in Windows Security > Device security > Core isolation, then restart (needed for attestation level B)"),
	}
	return cs
}

// registerVsock maps the Hyper-V socket service GUIDs of the vsock ports in use
// (7000 for control, the uplink ports for the control plane) so the Linux guest's
// AF_VSOCK sockets reach this host.
func (h *hypervDriver) registerVsock() error {
	for _, port := range append([]int{vsockPort}, h.cfg.uplinkPorts()...) {
		g := vsockrpc.VsockServiceID(uint32(port))
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
			`SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\`+g.String(), registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue("ElementName", fmt.Sprintf("Agentbox vsock port %d", port))
		k.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func q(s string) string { return strings.ReplaceAll(s, "'", "''") }

func (h *hypervDriver) Start(ctx context.Context) error {
	if err := h.registerVsock(); err != nil {
		return fmt.Errorf("register hvsock service: %w", err)
	}
	dir := h.cfg.StateDir
	base := h.cfg.BaseImage
	diff := filepath.Join(dir, "diff.vhdx")
	data := filepath.Join(dir, "data.vhdx")
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$name = '%[1]s'; $dir = '%[2]s'; $base = '%[3]s'; $diff = '%[4]s'; $data = '%[5]s'
if (-not (Test-Path -LiteralPath $base)) { throw "appliance disk not found: $base" }
$vm = Get-VM -Name $name -ErrorAction SilentlyContinue
if ($vm -and $vm.State -eq 'Running') { return }
if ($vm) {
  # boot from the signed base again: discard the differencing disk
  if ($vm.State -ne 'Off') { Stop-VM -Name $name -TurnOff -Force }
  Get-VMHardDiskDrive -VMName $name | Where-Object { $_.Path -eq $diff } | Remove-VMHardDiskDrive
  Remove-Item -LiteralPath $diff -Force -ErrorAction SilentlyContinue
  New-VHD -Path $diff -ParentPath $base -Differencing | Out-Null
  Add-VMHardDiskDrive -VMName $name -Path $diff -ControllerType SCSI -ControllerNumber 0 -ControllerLocation 0
  Set-VMFirmware -VMName $name -FirstBootDevice (Get-VMHardDiskDrive -VMName $name | Where-Object { $_.Path -eq $diff })
} else {
  Remove-Item -LiteralPath $diff -Force -ErrorAction SilentlyContinue
  New-VHD -Path $diff -ParentPath $base -Differencing | Out-Null
  if (-not (Test-Path -LiteralPath $data)) { New-VHD -Path $data -SizeBytes %[6]dGB -Dynamic | Out-Null }
  New-VM -Name $name -Generation 2 -MemoryStartupBytes %[7]dMB -VHDPath $diff -SwitchName '%[8]s' | Out-Null
  Set-VM -Name $name -ProcessorCount %[9]d -StaticMemory -CheckpointType Disabled -AutomaticCheckpointsEnabled $false -AutomaticStopAction TurnOff -AutomaticStartAction Nothing
  Add-VMHardDiskDrive -VMName $name -Path $data -ControllerType SCSI
  Set-VMFirmware -VMName $name -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority
  Set-VMKeyProtector -VMName $name -NewLocalKeyProtector
  Enable-VMTPM -VMName $name
  Disable-VMIntegrationService -VMName $name -Name 'Guest Service Interface','Key-Value Pair Exchange'
  Enable-VMIntegrationService -VMName $name -Name 'Heartbeat','Time Synchronization'
}
# Static memory must be free at start: when the PC is short of RAM (other VMs, Docker,
# games) fall back to smaller sizes rather than failing.
$sizes = @(%[7]d, 3072, 2048, 1536) | Where-Object { $_ -le %[7]d } | Select-Object -Unique
$started = $false
foreach ($mb in $sizes) {
  Set-VM -Name $name -MemoryStartupBytes ($mb * 1MB)
  try { Start-VM -Name $name -ErrorAction Stop; $started = $true; Write-Output "started with $mb MB"; break }
  catch { $last = $_ }
}
if (-not $started) { throw $last }`, vmName, q(dir), q(base), q(diff), q(data), h.cfg.DataGB, h.cfg.MemoryMB, q(h.cfg.SwitchName), h.cfg.CPUs)
	if out, err := ps(ctx, script); err != nil {
		return fmt.Errorf("start the appliance VM: %v: %s", err, out)
	}
	h.mu.Lock()
	h.vmID = ""
	h.mu.Unlock()
	return nil
}

// Stop turns the VM off (no saved memory state on disk).
func (h *hypervDriver) Stop(ctx context.Context) error {
	_, err := ps(ctx, fmt.Sprintf(`$v = Get-VM -Name %s -ErrorAction SilentlyContinue; if ($v -and $v.State -ne 'Off') { Stop-VM -Name %s -TurnOff -Force }`, vmName, vmName))
	return err
}

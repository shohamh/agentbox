# Installs Agentbox on this Windows PC the way production does: the host service
# runs as a Windows service (LocalSystem) and owns a Hyper-V Generation 2
# appliance VM (Secure Boot, virtual TPM, differencing disk, Hyper-V sockets).
# The control plane for the test runs in Docker Desktop on this PC.
#
# Run from an ELEVATED PowerShell (Run as administrator):
#   powershell -ExecutionPolicy Bypass -File scripts\install-hyperv.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\install-hyperv.ps1 -Uninstall
#
# Before: bash scripts/build-windows.sh ; bash scripts/build-vm.sh (dist\vm\agentbox-appliance.vhdx
# and the signed release manifest in the console's releases volume) ; the control
# plane up (scripts/up.sh). Nothing is downloaded by this script.
param(
  [string]$DeviceId = "pc-win",
  [string]$Vhdx = (Join-Path $PSScriptRoot "..\dist\vm\agentbox-appliance.vhdx"),
  [string]$Binaries = (Join-Path $PSScriptRoot "..\dist\windows"),
  [int]$MemoryMB = 4096,
  [int]$Cpus = 4,
  [switch]$Uninstall
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
# Everything this script prints, plus diagnostics, lands in C:\ProgramData\Agentbox
# so it can be read without elevation (install.log, diag.txt).
New-Item -ItemType Directory -Force "C:\ProgramData\Agentbox" | Out-Null
try { Start-Transcript -Path "C:\ProgramData\Agentbox\install.log" -Force | Out-Null } catch { }
trap {
  Write-Host "FAILED: $($_.Exception.Message)" -ForegroundColor Red
  try { & (Join-Path $PSScriptRoot "hyperv-diag.ps1") } catch { }
  try { Stop-Transcript | Out-Null } catch { }
  break
}
$bin = "C:\Program Files\Agentbox\bin"
$data = "C:\ProgramData\Agentbox"
$vmdir = Join-Path $data "vm"
$svcName = "agentbox-host"
$vmName = "agentbox-appliance"

# Windows PowerShell 5.1 turns any stderr line of a native command into a
# terminating error under $ErrorActionPreference = "Stop" (docker prints harmless
# warnings; agentbox.exe prints errors while the appliance boots). Run native
# commands through this and read $script:nativeExit.
function Native([scriptblock]$Block) {
  $prev = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try { & $Block; $script:nativeExit = $LASTEXITCODE } finally { $ErrorActionPreference = $prev }
}

$admin = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $admin.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw "Run this from an elevated PowerShell (right-click > Run as administrator): it creates a Hyper-V VM and a Windows service."
}

function Remove-Install {
  if (Get-Service $svcName -ErrorAction SilentlyContinue) {
    Stop-Service $svcName -Force -ErrorAction SilentlyContinue
    sc.exe delete $svcName | Out-Null
  }
  # A user-mode host from scripts\install-windows.ps1 would hold the named pipe.
  Get-Process agentbox-host -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
  $vm = Get-VM -Name $vmName -ErrorAction SilentlyContinue
  if ($vm) {
    if ($vm.State -ne "Off") { Stop-VM -Name $vmName -TurnOff -Force }
    Remove-VM -Name $vmName -Force
  }
  Remove-Item -Recurse -Force $vmdir, "C:\Program Files\Agentbox" -ErrorAction SilentlyContinue
}
if ($Uninstall) {
  Remove-Install
  Write-Host "Removed the service, the VM and the files (the data in $data\host.json and logs remain; delete $data by hand if you want them gone)."
  return
}

# ---- prerequisites ----
Write-Host "== checking prerequisites"
if ((Get-Service vmms -ErrorAction SilentlyContinue).Status -ne "Running") {
  throw "Hyper-V is not running. Enable it (Windows Features > Hyper-V, or: Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All), restart Windows, then run this again."
}
$feat = (Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All -ErrorAction SilentlyContinue).State
if ($feat -and $feat -ne "Enabled") { throw "Hyper-V feature state is '$feat': enable it and restart first." }
foreach ($f in @($Vhdx, (Join-Path $Binaries "agentbox-host.exe"), (Join-Path $Binaries "agentbox.exe"))) {
  if (-not (Test-Path $f)) { throw "missing $f : build it first (bash scripts/build-windows.sh ; bash scripts/build-vm.sh)" }
}
$sw = Get-VMSwitch -Name "Default Switch" -ErrorAction SilentlyContinue
if (-not $sw) { throw "The Hyper-V 'Default Switch' does not exist. Restart the 'Hyper-V Virtual Machine Management' service or Windows." }
Write-Host "   Hyper-V ok. The appliance reaches the control plane through this PC over Hyper-V sockets (no network address or firewall rule involved)."

# ---- remove a previous install, then lay down files ----
Remove-Install
New-Item -ItemType Directory -Force $bin, $vmdir | Out-Null
Copy-Item -Force (Join-Path $Binaries "agentbox-host.exe"), (Join-Path $Binaries "agentbox.exe") $bin
Copy-Item -Force $Vhdx (Join-Path $vmdir "agentbox-appliance.vhdx")
# The VM worker process must be able to open the disks.
icacls $vmdir /grant "NT VIRTUAL MACHINE\Virtual Machines:(OI)(CI)F" | Out-Null

# ---- control plane reachable from the VM ----
Write-Host "== publishing the control-plane ports on loopback (the host service relays them to the VM)"
$envFile = Join-Path $repo "deploy\compose\.secrets\compose.env"
if (-not (Test-Path $envFile)) { throw "$envFile not found: bring the control plane up first (bash scripts/up.sh)" }
$compose = @("compose", "-p", "agentbox", "-f", (Join-Path $repo "deploy\compose\docker-compose.yml"), "-f", (Join-Path $repo "deploy\compose\hyperv.yml"), "--env-file", $envFile, "up", "-d")
$composeOut = Native { & docker @compose 2>&1 | ForEach-Object { "$_" } }
$composeOut | Where-Object { $_ -notmatch "was not created by Docker Compose" } | Select-Object -Last 6
if ($script:nativeExit -ne 0) { Write-Warning "docker compose failed here (is Docker Desktop running for this user?). Run it yourself: docker $($compose -join ' ')" }

# ---- the corporate root CA, for the host service ----
$ca = Join-Path $data "root_ca.crt"
$pem = docker run --rm -v agentbox_pki:/p:ro debian:bookworm-slim cat /p/ca.crt
if (-not ($pem -match "BEGIN CERTIFICATE")) { throw "could not read the root CA from the agentbox_pki volume" }
Set-Content -Encoding ascii $ca $pem

# ---- configuration and service ----
$cfg = [ordered]@{
  driver               = "hyperv"
  state_dir            = $vmdir
  base_image           = (Join-Path $vmdir "agentbox-appliance.vhdx")
  device_id            = $DeviceId
  memory_mb            = $MemoryMB
  cpus                 = $Cpus
  switch_name          = "Default Switch"
  enroll_url           = "https://enroll.agentbox.corp:18443"
  oidc_issuer          = "http://kc.agentbox.corp:8080/realms/agentbox"
  ca_root_file         = $ca
  allow_password_grant = $true
  log_file             = (Join-Path $data "host.log")
  host_map             = [ordered]@{
    "enroll.agentbox.corp:18443" = "127.0.0.1:18443"
    "kc.agentbox.corp:8080"      = "127.0.0.1:18180"
  }
}
$cfgPath = Join-Path $data "host.json"
[IO.File]::WriteAllText($cfgPath, ($cfg | ConvertTo-Json -Depth 4), (New-Object Text.UTF8Encoding $false))

Write-Host "== installing the $svcName service (LocalSystem, automatic start)"
New-Service -Name $svcName -DisplayName "Agentbox host service" -BinaryPathName "`"$bin\agentbox-host.exe`" `"$cfgPath`"" -StartupType Automatic | Out-Null
Start-Service $svcName
Write-Host "   started; it creates and boots the appliance VM '$vmName' (first boot takes about a minute)"

# ---- wait for the appliance ----
$cli = Join-Path $bin "agentbox.exe"
$deadline = (Get-Date).AddMinutes(6)
$up = $false
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Seconds 5
  $out = (Native { & $cli status 2>&1 | ForEach-Object { "$_" } }) | Out-String
  if ($out -match "appliance\s+release") { $up = $true; break }
}
Write-Host ""
Native { & $cli status 2>&1 | ForEach-Object { "$_" } }
if (-not $up) {
  try { & (Join-Path $PSScriptRoot "hyperv-diag.ps1") } catch { }
  try { Stop-Transcript | Out-Null } catch { }
  Write-Warning "The appliance did not answer within 6 minutes."
  Write-Host "Look at:  Get-Content $data\host.log -Tail 40 ; Get-VM $vmName ; & '$cli' doctor"
  Write-Host "(a dev-flavor image has a serial console: bash scripts/build-vm.sh with FLAVOR=dev)"
  exit 1
}
try { & (Join-Path $PSScriptRoot "hyperv-diag.ps1") } catch { }
try { Stop-Transcript | Out-Null } catch { }
Write-Host ""
Write-Host "Next: enroll this PC and start a sandbox (from a normal, non-elevated shell):"
Write-Host "  `"Passw0rd!`" | & '$cli' enroll --user alice --password-stdin"
Write-Host "  & '$cli' new --name demo --image codex ; & '$cli' shell demo -- uname -a"

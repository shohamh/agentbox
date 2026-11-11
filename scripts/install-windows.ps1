# Installs the Agentbox PC side on this Windows PC for the container test bed:
# agentbox.exe (CLI) and agentbox-host.exe (host service), plus config. Runs as
# the current user: no elevation. The appliance runs in Docker Desktop (see
# deploy/compose/windows.yml); production uses a Hyper-V VM instead.
#
#   scripts\install-windows.ps1            install and start the host service
#   scripts\install-windows.ps1 -Uninstall stop it and remove the install
param(
  [string]$DeviceId = "pc-win",
  [string]$Source = (Join-Path $PSScriptRoot "..\dist\windows"),
  [switch]$Uninstall
)
$ErrorActionPreference = "Stop"
$root = Join-Path $env:LOCALAPPDATA "Agentbox"
$bin = Join-Path $root "bin"
$state = Join-Path $root "state"

Get-Process agentbox-host -ErrorAction SilentlyContinue | Stop-Process -Force
if ($Uninstall) {
  Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
  Write-Host "Removed $root"
  return
}

New-Item -ItemType Directory -Force $bin, $state | Out-Null
foreach ($f in "agentbox.exe", "agentbox-host.exe") {
  Copy-Item -Force (Join-Path $Source $f) $bin
}

# The corporate root CA, from the step-ca volume of the test bed.
$ca = Join-Path $state "root_ca.crt"
docker run --rm -v agentbox_pki:/p:ro debian:bookworm-slim cat /p/ca.crt | Set-Content -Encoding ascii $ca
if (-not (Select-String -Path $ca -Pattern "BEGIN CERTIFICATE" -Quiet)) { throw "could not read the root CA from the agentbox_pki volume: is the control plane up?" }

$cfg = [ordered]@{
  driver               = "container"
  appliance_addr       = "tcp://127.0.0.1:17000"
  state_dir            = $state
  device_id            = $DeviceId
  enroll_url           = "https://enroll.agentbox.corp:8443"
  oidc_issuer          = "http://kc.agentbox.corp:8080/realms/agentbox"
  ca_root_file         = $ca
  allow_password_grant = $true
  # This PC cannot resolve the corporate names without editing the hosts file
  # (needs admin): redirect only the TCP destination.
  host_map             = [ordered]@{
    "enroll.agentbox.corp:8443" = "127.0.0.1:18443"
    "kc.agentbox.corp:8080"     = "127.0.0.1:18180"
  }
}
$cfgPath = Join-Path $root "host.json"
[IO.File]::WriteAllText($cfgPath, ($cfg | ConvertTo-Json -Depth 4), (New-Object Text.UTF8Encoding $false))

$log = Join-Path $root "host.log"
Start-Process -FilePath (Join-Path $bin "agentbox-host.exe") -ArgumentList "`"$cfgPath`"" -WindowStyle Hidden `
  -RedirectStandardError $log -RedirectStandardOutput (Join-Path $root "host.out.log")
Start-Sleep -Seconds 2
if (-not (Get-Process agentbox-host -ErrorAction SilentlyContinue)) { Get-Content $log; throw "agentbox-host.exe did not start" }
Write-Host "Installed to $root. Try:  $bin\agentbox.exe status"

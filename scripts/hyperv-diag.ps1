# Collects everything needed to debug the Hyper-V install into
# C:\ProgramData\Agentbox\diag.txt (readable without elevation). Safe to run any
# time; run it elevated so the VM details are included. No secrets are written.
$ErrorActionPreference = "Continue"
$data = "C:\ProgramData\Agentbox"
New-Item -ItemType Directory -Force $data | Out-Null
$out = Join-Path $data "diag.txt"
$cli = "C:\Program Files\Agentbox\bin\agentbox.exe"

function Section($title, [scriptblock]$b) {
  "`n=== $title"
  try { & $b 2>&1 | ForEach-Object { "$_" } } catch { "error: $($_.Exception.Message)" }
}

$lines = & {
  "agentbox hyper-v diagnostics, $(Get-Date -Format s)"
  $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  "elevated: $isAdmin"
  Section "files" {
    foreach ($f in "C:\Program Files\Agentbox\bin\agentbox-host.exe", "C:\Program Files\Agentbox\bin\agentbox.exe", "$data\vm\agentbox-appliance.vhdx", "$data\vm\diff.vhdx", "$data\vm\data.vhdx", "$data\host.json") {
      if (Test-Path $f) { $i = Get-Item $f; "{0}  {1:N0} bytes  {2}" -f $f, $i.Length, $i.LastWriteTime } else { "$f  MISSING" }
    }
  }
  Section "service" {
    Get-CimInstance Win32_Service -Filter "Name='agentbox-host'" | Format-List Name, State, ExitCode, StartName, PathName | Out-String
    Get-WinEvent -FilterHashtable @{LogName = 'System'; ProviderName = 'Service Control Manager'; StartTime = (Get-Date).AddHours(-3)} -MaxEvents 20 -ErrorAction SilentlyContinue |
      Where-Object { $_.Message -match 'agentbox' } | ForEach-Object { "$($_.TimeCreated) [$($_.Id)] $($_.Message -replace '\s+', ' ')" }
  }
  Section "host.log (tail)" { Get-Content "$data\host.log" -Tail 60 -ErrorAction SilentlyContinue }
  Section "virtual machine" {
    $vm = Get-VM -Name agentbox-appliance -ErrorAction Stop
    $vm | Format-List Name, State, Status, Uptime, Generation, ProcessorCount, MemoryStartup, Heartbeat | Out-String
    "secure boot: " + (Get-VMFirmware -VMName agentbox-appliance).SecureBoot + "  template: " + (Get-VMFirmware -VMName agentbox-appliance).SecureBootTemplate
    "vTPM: " + (Get-VMSecurity -VMName agentbox-appliance).TpmEnabled
    Get-VMHardDiskDrive -VMName agentbox-appliance | ForEach-Object { "disk: " + $_.Path }
    Get-VMNetworkAdapter -VMName agentbox-appliance | ForEach-Object { "nic: switch=" + $_.SwitchName + " mac=" + $_.MacAddress + " ips=" + ($_.IPAddresses -join ",") }
    Get-VMIntegrationService -VMName agentbox-appliance | ForEach-Object { "integration: " + $_.Name + " enabled=" + $_.Enabled + " state=" + $_.PrimaryStatusDescription }
    "hvsock registry: " + (Test-Path "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\00001b58-facb-11e6-bd58-64006a7986d3")
  }
  Section "control-plane ports published on loopback (relayed to the VM over vsock)" {
    foreach ($p in 443, 8443, 4318, 5000, 18443) {
      $r = Test-NetConnection -ComputerName 127.0.0.1 -Port $p -WarningAction SilentlyContinue
      "127.0.0.1:${p} reachable=$($r.TcpTestSucceeded)"
    }
    "hvsock service keys registered: " + @(Get-ChildItem "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices" -ErrorAction SilentlyContinue | Where-Object { $_.GetValue('ElementName') -like 'Agentbox*' }).Count
  }
  Section "agentbox doctor" { if (Test-Path $cli) { & $cli doctor } else { "agentbox.exe not installed" } }
}
$lines | Set-Content -Encoding utf8 $out
Write-Host "diagnostics written to $out"

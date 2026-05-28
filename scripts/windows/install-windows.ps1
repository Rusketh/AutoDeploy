<#
.SYNOPSIS
    Install AutoDeploy as a Windows Service.

.DESCRIPTION
    Installs autodeploy-server.exe to Program Files, creates a data
    directory under ProgramData, registers the binary as a Windows
    Service named "autodeploy", applies environment variables from
    autodeploy.env (so the operator edits one file), and opens the
    Windows firewall for the HTTPS / TFTP listeners.

    The script is idempotent: re-running it upgrades the binary,
    re-applies env vars, and refreshes firewall rules. The data
    directory and database are preserved across re-runs.

    Run from an elevated PowerShell prompt (Run as Administrator).

.PARAMETER Binary
    Path to autodeploy-server-windows-amd64.exe (or -arm64.exe). If
    omitted, the script looks in the current directory.

.PARAMETER EnvFile
    Path to autodeploy.env. If omitted, the script looks alongside
    itself (scripts/windows/autodeploy.env) and falls back to
    autodeploy.env.example with a notice.

.PARAMETER InstallDir
    Where the binary is placed. Default: $env:ProgramFiles\AutoDeploy.

.PARAMETER DataDir
    Persistent state root. Default: $env:ProgramData\AutoDeploy. This
    value is also written into the service environment as
    AUTODEPLOY_DATA_DIR unless the env file already specifies one.

.PARAMETER ServiceAccount
    Account the service runs as. Default: LocalSystem (so binding to
    privileged ports 80/443/69 works without extra configuration).
    Use "NT AUTHORITY\NetworkService" for a less-privileged account
    if you don't need privileged-port binding.

.PARAMETER NoFirewall
    Skip creating Windows Firewall inbound rules.

.PARAMETER NoStart
    Install but don't start the service. Use to review configuration
    before launch.

.PARAMETER ApplyEnv
    Re-read autodeploy.env and re-apply the environment to an
    already-installed service, without copying the binary. Useful
    after editing the env file.

.EXAMPLE
    .\install-windows.ps1
        Installs from autodeploy-server-windows-amd64.exe in the
        current directory using default paths.

.EXAMPLE
    .\install-windows.ps1 -ApplyEnv
        Re-applies environment variables from autodeploy.env to an
        existing service install and restarts it.

.EXAMPLE
    .\install-windows.ps1 -Binary C:\downloads\autodeploy-server-windows-amd64.exe -DataDir D:\AutoDeploy
        Installs with a non-default data directory on D:\.
#>

[CmdletBinding()]
param(
    [string]$Binary,
    [string]$EnvFile,
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'AutoDeploy'),
    [string]$DataDir    = (Join-Path $env:ProgramData 'AutoDeploy'),
    [string]$ServiceAccount = 'LocalSystem',
    [switch]$NoFirewall,
    [switch]$NoStart,
    [switch]$NoIPXE,
    [switch]$ApplyEnv
)

$ErrorActionPreference = 'Stop'
$ServiceName = 'autodeploy'
$ServiceDisplayName = 'AutoDeploy server'
$ServiceDescription = 'AutoDeploy management portal, JSON API, payload service, Domain Integration Service and optional TFTP listener.'

function Write-Section($msg) {
    Write-Host ""
    Write-Host "== $msg ==" -ForegroundColor Cyan
}

function Require-Admin {
    $current = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($current)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "This script must be run from an elevated (Administrator) PowerShell prompt."
    }
}

function Resolve-BinarySource {
    param([string]$Hint)
    if ($Hint) {
        if (-not (Test-Path -LiteralPath $Hint)) {
            throw "Server binary not found at $Hint."
        }
        return (Resolve-Path -LiteralPath $Hint).Path
    }
    $arch = if ([Environment]::Is64BitOperatingSystem) {
        if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
    } else { throw "AutoDeploy is 64-bit only." }
    $candidate = Join-Path (Get-Location) "autodeploy-server-windows-$arch.exe"
    if (Test-Path -LiteralPath $candidate) { return (Resolve-Path -LiteralPath $candidate).Path }
    throw "Could not find autodeploy-server-windows-$arch.exe in $(Get-Location). Pass -Binary <path>."
}

function Resolve-EnvFile {
    param([string]$Hint)
    if ($Hint) {
        if (-not (Test-Path -LiteralPath $Hint)) {
            throw "Env file not found at $Hint."
        }
        return (Resolve-Path -LiteralPath $Hint).Path
    }
    $here = Split-Path -Parent $MyInvocation.PSCommandPath
    $live = Join-Path $here 'autodeploy.env'
    if (Test-Path -LiteralPath $live) { return (Resolve-Path -LiteralPath $live).Path }
    $example = Join-Path $here 'autodeploy.env.example'
    if (Test-Path -LiteralPath $example) {
        Write-Warning "Using $example as the env file. Copy it to autodeploy.env and edit before production use."
        return (Resolve-Path -LiteralPath $example).Path
    }
    throw "No autodeploy.env found next to install-windows.ps1. Pass -EnvFile <path>."
}

# Reads a KEY=VALUE-per-line file into a hashtable. Lines starting
# with # are ignored; blank lines are ignored; values are not quoted.
function Read-EnvFile {
    param([string]$Path)
    $map = [ordered]@{}
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith('#')) { return }
        $eq = $line.IndexOf('=')
        if ($eq -lt 1) { return }
        $key = $line.Substring(0, $eq).Trim()
        $val = $line.Substring($eq + 1)
        # Strip an optional trailing carriage return / surrounding quotes.
        $val = $val -replace "`r$", ''
        if (($val.StartsWith('"') -and $val.EndsWith('"')) -or ($val.StartsWith("'") -and $val.EndsWith("'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        $map[$key] = $val
    }
    return $map
}

function Apply-ServiceEnv {
    param([string]$Service, [hashtable]$Env)
    # The SCM reads this REG_MULTI_SZ at service start and merges the
    # entries into the new process's environment block.
    $regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$Service"
    if (-not (Test-Path $regPath)) {
        throw "Service registry key not found at $regPath. Did service registration succeed?"
    }
    $lines = @()
    foreach ($k in $Env.Keys) { $lines += "$k=$($Env[$k])" }
    # New-ItemProperty / Set-ItemProperty with -Type MultiString writes REG_MULTI_SZ.
    Set-ItemProperty -Path $regPath -Name 'Environment' -Value $lines -Type MultiString
}

Require-Admin

Write-Section "Resolving inputs"
$BinarySrc = Resolve-BinarySource -Hint $Binary
$EnvSrc    = Resolve-EnvFile     -Hint $EnvFile
Write-Host "Binary:   $BinarySrc"
Write-Host "Env file: $EnvSrc"
Write-Host "InstallDir: $InstallDir"
Write-Host "DataDir:    $DataDir"
Write-Host "Service account: $ServiceAccount"

# Read env file early so we know what AUTODEPLOY_DATA_DIR /
# AUTODEPLOY_*ADDR the operator wants -- this drives firewall rules
# and the default data dir.
$envMap = Read-EnvFile -Path $EnvSrc
if (-not $envMap.Contains('AUTODEPLOY_DATA_DIR') -or -not $envMap['AUTODEPLOY_DATA_DIR']) {
    $envMap['AUTODEPLOY_DATA_DIR'] = $DataDir
} else {
    # If the env file specifies a data dir, that wins -- the operator
    # had a reason to put it there.
    $DataDir = $envMap['AUTODEPLOY_DATA_DIR']
}

if ($ApplyEnv) {
    Write-Section "Re-applying environment variables to existing service"
    if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
        throw "Service '$ServiceName' is not installed. Run install-windows.ps1 without -ApplyEnv first."
    }
    Apply-ServiceEnv -Service $ServiceName -Env $envMap
    Write-Host "Restarting service to pick up new environment..."
    Restart-Service -Name $ServiceName -Force
    Write-Host "Done."
    exit 0
}

Write-Section "Creating directories"
foreach ($dir in @($InstallDir, $DataDir, (Join-Path $DataDir 'ipxe'), (Join-Path $DataDir 'downloads'))) {
    if (-not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Path $dir | Out-Null
        Write-Host "Created $dir"
    }
}

# Seed the downloads directory with any agent / boot client binaries
# that shipped in the same install bundle, so the portal's Downloads
# page works out of the box.
$DownloadsDir = Join-Path $DataDir 'downloads'
$candidates = @(
    'autodeploy-agent-windows-amd64.exe',
    'autodeploy-agent-windows-arm64.exe',
    'autodeploy-agent-linux-amd64',
    'autodeploy-boot-linux-amd64',
    'autodeploy-boot-linux-arm64'
)
$BundleRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.PSCommandPath)
foreach ($name in $candidates) {
    foreach ($src in @((Join-Path $BundleRoot $name), (Join-Path (Get-Location) $name))) {
        if (Test-Path -LiteralPath $src) {
            $dst = Join-Path $DownloadsDir $name
            if (-not (Test-Path -LiteralPath $dst)) {
                Copy-Item -LiteralPath $src -Destination $dst -Force
                Write-Host "    seeded $dst"
            }
            break
        }
    }
}

Write-Section "Copying binary"
$BinaryDst = Join-Path $InstallDir 'autodeploy-server.exe'
# Stop the service before overwriting the binary so the file isn't
# locked by a running process during upgrade.
if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    if ((Get-Service -Name $ServiceName).Status -eq 'Running') {
        Write-Host "Stopping running service for upgrade..."
        Stop-Service -Name $ServiceName -Force
    }
}
Copy-Item -LiteralPath $BinarySrc -Destination $BinaryDst -Force
Write-Host "Installed $BinaryDst"

# Also drop the env file alongside the binary so re-runs without
# arguments can find it, and so the operator has one well-known edit
# location.
$EnvDst = Join-Path $InstallDir 'autodeploy.env'
if (-not (Test-Path -LiteralPath $EnvDst)) {
    Copy-Item -LiteralPath $EnvSrc -Destination $EnvDst
    Write-Host "Installed $EnvDst (edit this and re-run with -ApplyEnv to change settings)"
}

if (-not $NoIPXE) {
    Write-Section "Fetching iPXE bootstrap binaries"
    $fetcher = Join-Path (Split-Path -Parent $MyInvocation.PSCommandPath) 'fetch-ipxe.ps1'
    if (Test-Path -LiteralPath $fetcher) {
        # Don't abort the installer if the network is unreachable -- the
        # operator can re-run fetch-ipxe.ps1 later. The child script
        # may exit 1; check $LASTEXITCODE rather than rely on try/catch
        # since exit codes don't surface as PowerShell exceptions.
        try {
            & $fetcher -DataDir $DataDir
            if ($LASTEXITCODE -ne 0 -and $null -ne $LASTEXITCODE) {
                throw "fetch-ipxe.ps1 returned exit code $LASTEXITCODE"
            }
        } catch {
            Write-Warning "iPXE fetch failed: $($_.Exception.Message). The server still installs; re-run scripts/windows/fetch-ipxe.ps1 once you have network access, or drop the binaries into $DataDir\ipxe by hand."
        }
    } else {
        Write-Warning "fetch-ipxe.ps1 not found at $fetcher; skipping iPXE download. Drop the binaries into $DataDir\ipxe yourself when ready."
    }
}

Write-Section "Registering Windows Service"
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "Service '$ServiceName' already exists -- updating binPath."
    # sc.exe is the only built-in tool that lets us change binPath.
    & sc.exe config $ServiceName binPath= "`"$BinaryDst`"" obj= $ServiceAccount | Out-Null
} else {
    & sc.exe create $ServiceName binPath= "`"$BinaryDst`"" `
        DisplayName= "$ServiceDisplayName" `
        start= auto `
        obj= $ServiceAccount | Out-Null
    & sc.exe description $ServiceName "$ServiceDescription" | Out-Null
    # Restart on failure: 1st/2nd in 5s, third gives up (matches the
    # systemd unit's Restart=on-failure RestartSec=5 intent).
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/`"`" | Out-Null
}

Write-Section "Applying environment variables"
Apply-ServiceEnv -Service $ServiceName -Env $envMap

if (-not $NoFirewall) {
    Write-Section "Configuring Windows Firewall"
    $rules = @(
        @{ Name = 'AutoDeploy HTTPS (TCP 443)'; Proto = 'TCP'; Port = 443 },
        @{ Name = 'AutoDeploy HTTP  (TCP 80)';  Proto = 'TCP'; Port = 80  },
        @{ Name = 'AutoDeploy TFTP  (UDP 69)';  Proto = 'UDP'; Port = 69  }
    )
    foreach ($r in $rules) {
        if (Get-NetFirewallRule -DisplayName $r.Name -ErrorAction SilentlyContinue) {
            Write-Host "Rule already present: $($r.Name)"
            continue
        }
        New-NetFirewallRule -DisplayName $r.Name `
            -Direction Inbound -Action Allow `
            -Protocol $r.Proto -LocalPort $r.Port `
            -Program $BinaryDst | Out-Null
        Write-Host "Created rule: $($r.Name)"
    }
}

if (-not $NoStart) {
    Write-Section "Starting service"
    Start-Service -Name $ServiceName
    # Give the service a moment to come up before we report status.
    Start-Sleep -Seconds 2
    Get-Service -Name $ServiceName | Format-Table -AutoSize
}

Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host " AutoDeploy is installed."                                    -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Write-Host ""
Write-Host "Edit configuration:    $EnvDst"
Write-Host "Then re-apply with:    .\install-windows.ps1 -ApplyEnv"
Write-Host ""
Write-Host "First-time bootstrap admin password (one-time):"
Write-Host "    Get-Content '$DataDir\admin-bootstrap.txt'"
Write-Host "Read it, log in at https://<this-host>/portal/, change the"
Write-Host "password in Settings -> Local accounts, then delete the file."
Write-Host ""
Write-Host "Service control:"
Write-Host "    Get-Service $ServiceName"
Write-Host "    Restart-Service $ServiceName"
Write-Host "    Stop-Service $ServiceName"
Write-Host ""
Write-Host "Service logs (the binary writes structured JSON to its event"
Write-Host "stream; the SCM captures it -- view recent output with):"
Write-Host "    Get-WinEvent -ProviderName 'Service Control Manager' -MaxEvents 20"
Write-Host ""

<#
.SYNOPSIS
    Remove the AutoDeploy Windows service and firewall rules.

.DESCRIPTION
    Stops the service, deletes the SCM registration, removes the
    firewall rules and (optionally) deletes the install directory.
    The data directory is preserved by default so an accidental
    uninstall does not wipe the database -- pass -PurgeData to also
    delete C:\ProgramData\AutoDeploy.

    Run from an elevated PowerShell prompt.
#>

[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'AutoDeploy'),
    [string]$DataDir    = (Join-Path $env:ProgramData 'AutoDeploy'),
    [switch]$PurgeData
)

$ErrorActionPreference = 'Stop'
$ServiceName = 'autodeploy'

$current = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not (New-Object Security.Principal.WindowsPrincipal($current)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Must be run from an elevated (Administrator) PowerShell prompt."
}

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    if ((Get-Service -Name $ServiceName).Status -eq 'Running') {
        Write-Host "Stopping service..."
        Stop-Service -Name $ServiceName -Force
    }
    Write-Host "Removing service registration..."
    & sc.exe delete $ServiceName | Out-Null
} else {
    Write-Host "Service '$ServiceName' is not installed -- nothing to remove."
}

foreach ($name in @(
    'AutoDeploy HTTPS (TCP 443)',
    'AutoDeploy HTTP  (TCP 80)',
    'AutoDeploy TFTP  (UDP 69)'
)) {
    if (Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue) {
        Remove-NetFirewallRule -DisplayName $name
        Write-Host "Removed firewall rule: $name"
    }
}

if (Test-Path -LiteralPath $InstallDir) {
    Write-Host "Removing $InstallDir"
    Remove-Item -LiteralPath $InstallDir -Recurse -Force
}

if ($PurgeData) {
    if (Test-Path -LiteralPath $DataDir) {
        Write-Warning "PurgeData: deleting $DataDir (database, secrets key, payloads ALL go)"
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
} else {
    Write-Host ""
    Write-Host "Data directory preserved at: $DataDir"
    Write-Host "Pass -PurgeData to delete it as well (database, secrets key, payloads)."
}

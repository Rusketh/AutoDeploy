<#
.SYNOPSIS
    Fetch the iPXE network-boot binaries operators need to chainload
    AutoDeploy from a classic PXE / TFTP environment.

.DESCRIPTION
    PowerShell mirror of scripts/fetch-ipxe.sh. Downloads the pre-built
    iPXE bootstrap binaries from boot.ipxe.org into the iPXE directory
    under the configured AutoDeploy data directory, named per the
    iPXE.org convention so an operator can point a TFTP server's root
    directly at the directory.

    Files fetched:
        undionly.kpxe      - legacy BIOS PXE chainload (most common)
        ipxe.pxe           - alternative BIOS chainload (use when
                             undionly.kpxe hangs on the NIC)
        ipxe.efi           - UEFI x64 chainload
        snponly.efi        - UEFI x64 chainload via the SNP driver (use
                             when ipxe.efi cannot bring the NIC up)
        ipxe-arm64.efi     - UEFI arm64 chainload

    If the environment cannot reach the internet, build iPXE yourself
    from https://github.com/ipxe/ipxe and drop the binaries in this
    directory by hand.

.PARAMETER DataDir
    Where to write the binaries. Default: $env:ProgramData\AutoDeploy.
    The script writes into the iPXE subdirectory of this path.

.PARAMETER Force
    Re-download even if the file already exists.

.EXAMPLE
    .\fetch-ipxe.ps1
        Fetches all binaries to C:\ProgramData\AutoDeploy\ipxe\.

.EXAMPLE
    .\fetch-ipxe.ps1 -DataDir D:\AutoDeploy -Force
        Re-fetches into D:\AutoDeploy\ipxe\, overwriting existing files.
#>

[CmdletBinding()]
param(
    [string]$DataDir = $(if ($env:AUTODEPLOY_DATA_DIR) { $env:AUTODEPLOY_DATA_DIR } else { Join-Path $env:ProgramData 'AutoDeploy' }),
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

$ipxeDir = Join-Path $DataDir 'ipxe'
if (-not (Test-Path -LiteralPath $ipxeDir)) {
    New-Item -ItemType Directory -Path $ipxeDir | Out-Null
}

# spec: [remote-path, local-filename]. When the two match the
# basename of remote-path is used.
$files = @(
    @{ Src = 'undionly.kpxe';        Dst = 'undionly.kpxe' }
    @{ Src = 'ipxe.pxe';             Dst = 'ipxe.pxe' }
    @{ Src = 'ipxe.efi';             Dst = 'ipxe.efi' }
    @{ Src = 'snponly.efi';          Dst = 'snponly.efi' }
    @{ Src = 'arm64-efi/ipxe.efi';   Dst = 'ipxe-arm64.efi' }
)

$base = 'http://boot.ipxe.org'

# Force TLS 1.2 in case Windows PowerShell defaults to an older
# version (the boot.ipxe.org redirect chain prefers modern TLS even
# on the http URL). Tls13 isn't available on older .NET Framework
# versions, so we stick to Tls12 which works everywhere we care.
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # Older PowerShell may not expose SecurityProtocol; ignore.
}

$failed = 0
foreach ($f in $files) {
    $dst = Join-Path $ipxeDir $f.Dst
    if ((Test-Path -LiteralPath $dst) -and -not $Force) {
        Write-Host "Skipping $($f.Dst) (already present; pass -Force to refetch)"
        continue
    }
    $url = "$base/$($f.Src)"
    Write-Host "Fetching $url -> $dst"
    try {
        Invoke-WebRequest -Uri $url -OutFile $dst -UseBasicParsing
    } catch {
        Write-Warning "Failed to fetch $($f.Dst): $($_.Exception.Message)"
        $failed++
        continue
    }
    # Sanity-check the file isn't an HTML error page.
    $size = (Get-Item -LiteralPath $dst).Length
    if ($size -lt 1024) {
        Write-Warning "$($f.Dst) is only $size bytes -- looks like an error response, not a real binary."
        $failed++
        continue
    }
}

Write-Host ""
Write-Host "iPXE binaries in $ipxeDir :"
Get-ChildItem -LiteralPath $ipxeDir | Format-Table -AutoSize Name, Length, LastWriteTime

if ($failed -gt 0) {
    Write-Warning "$failed download(s) failed -- check network reachability to $base or fetch them manually."
    exit 1
}

Write-Host ""
Write-Host "Next step: point your TFTP server (the built-in AutoDeploy TFTP"
Write-Host "listener, or a separate daemon) at $ipxeDir and configure DHCP."
Write-Host "See docs/user-guide/pxe-setup.md for the DHCP config patterns."

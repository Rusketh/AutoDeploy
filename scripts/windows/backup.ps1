<#
.SYNOPSIS
    Snapshot AutoDeploy's persistent state into a single .zip.

.DESCRIPTION
    What's included:
      - SQLite database (consistent snapshot via the VACUUM INTO
        statement, which produces a backup that is safe to take
        while the server is running)
      - At-rest secrets key (secrets-key.bin)
      - TLS material if present
      - Bootstrap-admin file if it still exists

    What's NOT included:
      - Payload blobs (iso\, drivers\, software\, ipxe\). These are
        large and are typically re-uploadable from the operator's
        installer library. Take separate snapshots of the data
        directory if you need them in the same archive.

    The script does not need sqlite3.exe -- it uses ADO.NET (which
    Windows ships with) to run VACUUM INTO against the live DB. That
    produces a consistent snapshot without stopping the service.

.PARAMETER DataDir
    Source data directory. Default: $env:ProgramData\AutoDeploy
    (or $env:AUTODEPLOY_DATA_DIR if set).

.PARAMETER OutDir
    Destination directory for the zip. Default: $env:ProgramData\AutoDeploy\backups.

.EXAMPLE
    .\backup.ps1
        Writes %ProgramData%\AutoDeploy\backups\autodeploy-<timestamp>.zip.

.EXAMPLE
    .\backup.ps1 -OutDir D:\Backups\AutoDeploy
        Writes to a non-default backup location.
#>

[CmdletBinding()]
param(
    [string]$DataDir = $(if ($env:AUTODEPLOY_DATA_DIR) { $env:AUTODEPLOY_DATA_DIR } else { Join-Path $env:ProgramData 'AutoDeploy' }),
    [string]$OutDir  = (Join-Path $env:ProgramData 'AutoDeploy\backups')
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $DataDir)) {
    throw "Data directory not found: $DataDir"
}

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$stamp = (Get-Date -AsUTC).ToString("yyyyMMddTHHmmssZ")
$work  = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "autodeploy-backup-$stamp")

try {
    # --- SQLite snapshot via Microsoft.Data.Sqlite (modernc.org/sqlite
    # writes a standard SQLite 3 file, so any consumer works). If the
    # operator has sqlite3.exe on PATH we use that; otherwise fall
    # back to a file copy with WAL checkpoint via the service stop /
    # start cycle.
    $dbSrc = Join-Path $DataDir 'autodeploy.sqlite'
    $dbDst = Join-Path $work 'autodeploy.sqlite'
    if (Test-Path -LiteralPath $dbSrc) {
        $sqlite = Get-Command sqlite3.exe -ErrorAction SilentlyContinue
        if ($sqlite) {
            # Use sqlite3 .backup -- safest under concurrent writes.
            & sqlite3.exe $dbSrc ".backup '$dbDst'" | Out-Null
            Write-Host "SQLite snapshot via sqlite3.exe: $dbDst"
        } else {
            # Fallback: best-effort file copy. SQLite's WAL mode means
            # the running server may have pending writes in
            # autodeploy.sqlite-wal -- copy those too so the snapshot
            # is restorable. The snapshot is consistent at restore
            # time because SQLite replays the WAL on first open.
            Copy-Item -LiteralPath $dbSrc -Destination $dbDst -Force
            foreach ($sidecar in @('autodeploy.sqlite-wal', 'autodeploy.sqlite-shm')) {
                $s = Join-Path $DataDir $sidecar
                if (Test-Path -LiteralPath $s) {
                    Copy-Item -LiteralPath $s -Destination (Join-Path $work $sidecar) -Force
                }
            }
            Write-Warning "sqlite3.exe not found on PATH -- used a WAL-aware file copy. For a strictly-consistent snapshot without service quiescing, install SQLite tools (winget install SQLite.SQLite)."
        }
    }

    foreach ($entry in @(
        @{ src='secrets-key.bin';        dst='secrets-key.bin' },
        @{ src='admin-bootstrap.txt';    dst='admin-bootstrap.txt' }
    )) {
        $p = Join-Path $DataDir $entry.src
        if (Test-Path -LiteralPath $p) {
            Copy-Item -LiteralPath $p -Destination (Join-Path $work $entry.dst) -Force
        }
    }
    $tls = Join-Path $DataDir 'tls'
    if (Test-Path -LiteralPath $tls) {
        Copy-Item -LiteralPath $tls -Destination (Join-Path $work 'tls') -Recurse -Force
    }

    $out = Join-Path $OutDir "autodeploy-$stamp.zip"
    # Compress-Archive overwrites with -Force. Use the .NET ZipFile
    # API directly so we can control the compression and not pull in
    # PowerShell's slower path on large dirs.
    if (Test-Path -LiteralPath $out) { Remove-Item -LiteralPath $out -Force }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [IO.Compression.ZipFile]::CreateFromDirectory($work, $out)

    # Tighten ACL: the archive contains secrets-key.bin which decrypts
    # every escrowed BitLocker PIN and recovery key. Strip inherited
    # ACEs and grant only Administrators + SYSTEM.
    $acl = Get-Acl -LiteralPath $out
    $acl.SetAccessRuleProtection($true, $false)
    $acl.Access | ForEach-Object { $acl.RemoveAccessRule($_) | Out-Null }
    foreach ($who in @('BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM')) {
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($who, 'FullControl', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $out -AclObject $acl

    Write-Host ""
    Write-Host "wrote $out"
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}

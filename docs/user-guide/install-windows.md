# Installing AutoDeploy on Windows

The AutoDeploy server runs natively as a Windows Service. This guide
walks through a fresh install on Windows Server 2019 / 2022 / 2025 or
a Windows 10 / 11 host. The end state is the same as the Linux
install: a service that starts on boot, binds the portal on HTTPS,
optionally serves TFTP, and stores its state under
`C:\ProgramData\AutoDeploy`.

## Before you start

You need:

- A Windows 10 / 11 or Server 2019+ host with PowerShell 5.1 or 7.
- Administrator on the install host.
- The AutoDeploy release artifacts (binary + scripts).
- Outbound HTTPS to whatever DC the AD service account lives on
  (only if you'll use AD integration; can be added later).
- A reachable hostname or IP. For lab use the host's IP is fine;
  for production, a DNS name + TLS certificate is strongly preferred.

The installer registers a native Windows Service that responds to
`Start-Service` / `Stop-Service` / SCM stop and shutdown controls.
Nothing extra (NSSM, WinSW, srvany) is needed — the binary speaks the
SCM protocol itself.

## Step 1 — Download

From a PowerShell prompt:

```powershell
$TAG = 'v1.0.0'   # replace with the release you want
$URL = "https://github.com/Rusketh/AutoDeploy/releases/download/$TAG"

# Binary (amd64; for ARM64 swap amd64 -> arm64)
Invoke-WebRequest "$URL/autodeploy-server-windows-amd64.exe" -OutFile autodeploy-server-windows-amd64.exe
Invoke-WebRequest "$URL/autodeploy-server-windows-amd64.exe.sha256" -OutFile autodeploy-server-windows-amd64.exe.sha256

# Operator scripts and docs
Invoke-WebRequest "$URL/autodeploy-extras.tar.gz" -OutFile autodeploy-extras.tar.gz
```

Verify the binary checksum:

```powershell
$expected = (Get-Content autodeploy-server-windows-amd64.exe.sha256).Split(' ')[0]
$actual   = (Get-FileHash autodeploy-server-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
if ($expected -ne $actual) { throw "Checksum mismatch!" }
"OK: $actual"
```

Extract the scripts (Windows 10 1803+ and Server 2019+ ship `tar.exe`):

```powershell
tar -xzf autodeploy-extras.tar.gz
```

You'll now have a `scripts\windows\` directory containing
`install-windows.ps1`, `uninstall-windows.ps1`, `backup.ps1` and
`autodeploy.env.example`.

## Step 2 — Run the installer

Open an **elevated** PowerShell prompt (right-click → Run as
Administrator) in the directory that contains the binary and run:

```powershell
.\scripts\windows\install-windows.ps1
```

The installer will:

1. Copy `autodeploy-server.exe` to `C:\Program Files\AutoDeploy\`.
2. Create the data directory at `C:\ProgramData\AutoDeploy\` (with
   `ipxe\` for the static iPXE assets and `downloads\` for portal-
   served binaries).
3. Drop a fresh `autodeploy.env` next to the binary if one doesn't
   already exist there (copied from `autodeploy.env.example`).
4. **Fetch the iPXE bootstrap binaries** (`undionly.kpxe`,
   `ipxe.efi`, `snponly.efi`, `ipxe.pxe`, `ipxe-arm64.efi`) from
   `boot.ipxe.org` into `C:\ProgramData\AutoDeploy\ipxe\` so the
   built-in TFTP listener can serve them straight away. Pass
   `-NoIPXE` to skip if your install host has no internet access;
   you can re-run the fetcher later with
   `.\scripts\windows\fetch-ipxe.ps1`.
5. Register the binary as a Windows Service called **autodeploy**,
   set to start automatically, running as **LocalSystem** by default.
6. Write the env-file values into the service's environment block in
   `HKLM\SYSTEM\CurrentControlSet\Services\autodeploy\Environment`.
7. Open Windows Firewall inbound rules for HTTPS (TCP 443),
   HTTP (TCP 80) and TFTP (UDP 69), scoped to the AutoDeploy binary.
8. Start the service.

When the installer finishes it prints the path to a one-time
bootstrap-admin file:

```
First-time bootstrap admin password (one-time):
    Get-Content 'C:\ProgramData\AutoDeploy\admin-bootstrap.txt'
```

Read it once. The default shape is HTTP on port 8080, so log in at
`http://<this-host>:8080/portal/` (or `https://<this-host>/portal/`
if you configured HTTPS), change the password under **Settings →
Local accounts**, and delete the bootstrap file.

## Step 3 — Configure

Edit `C:\Program Files\AutoDeploy\autodeploy.env`. The defaults are
sensible for a lab install; the bits worth reviewing for production:

```
# Bind addresses -- empty disables that surface.
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443
AUTODEPLOY_HTTP_ADDR=127.0.0.1:8080
AUTODEPLOY_TFTP_ADDR=:69

# Production cert + key. The dev mode self-signed cert is fine for
# a lab; do not run with dev mode in production.
AUTODEPLOY_TLS_CERT=C:\ProgramData\AutoDeploy\tls\server.crt
AUTODEPLOY_TLS_KEY=C:\ProgramData\AutoDeploy\tls\server.key
AUTODEPLOY_DEV=false

# Generate a 32-byte hex key once and keep a backup off-host.
AUTODEPLOY_SECRETS_KEY=...
```

To generate a secrets key:

```powershell
[Convert]::ToHexString(
    [System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
).ToLower()
```

After editing, re-apply the env to the service:

```powershell
.\scripts\windows\install-windows.ps1 -ApplyEnv
```

`-ApplyEnv` re-reads the env file, writes it to the service registry,
and restarts the service. It does NOT re-copy the binary.

## Step 4 — TLS certificate

For production use a real CA-signed certificate. Place the cert and
its private key as PEM-encoded files at the paths you put in
`AUTODEPLOY_TLS_CERT` / `AUTODEPLOY_TLS_KEY`, then restart:

```powershell
Restart-Service autodeploy
```

For a lab install you can leave dev mode on and AutoDeploy will
auto-generate a self-signed cert into
`C:\ProgramData\AutoDeploy\tls\` on first start.

## Step 5 — iPXE assets and the kernel/initrd

The installer's iPXE fetch step already populated
`C:\ProgramData\AutoDeploy\ipxe\` with the bootstrap binaries
(`undionly.kpxe`, `ipxe.efi`, `snponly.efi`, `ipxe.pxe`,
`ipxe-arm64.efi`). If you skipped that step with `-NoIPXE`, or if
you need to refresh after an iPXE release, re-run:

```powershell
.\scripts\windows\fetch-ipxe.ps1
```

You still have to drop the Boot Client kernel + initramfs into the
same directory so PXE-booted clients can chainload into the
AutoDeploy boot environment:

```
autodeploy-kernel   # Linux kernel built by scripts/initramfs
autodeploy-initrd   # initramfs containing autodeploy-boot
```

Those are produced by `scripts/initramfs/build-initramfs.sh` on a
Linux host (the build is Linux-only); copy the two files into
`C:\ProgramData\AutoDeploy\ipxe\` after building.

Configure your DHCP scope to hand out `undionly.kpxe` for BIOS clients
and `ipxe.efi` for UEFI clients, with the server option pointing at
this host. See `docs/user-guide/pxe-setup.md` for the full DHCP
recipe (vendor-class branching, etc.).

## Day-to-day operation

```powershell
# Status
Get-Service autodeploy

# Restart after config changes
Restart-Service autodeploy

# Recent service-control activity (start, stop, crashes)
Get-WinEvent -ProviderName 'Service Control Manager' -MaxEvents 20 |
    Where-Object { $_.Message -match 'autodeploy' }

# Stream the server's own stdout. The Windows SCM does not capture
# stdout for normal services, so for live tailing run the binary
# interactively in a separate elevated PowerShell:
#   Stop-Service autodeploy
#   & 'C:\Program Files\AutoDeploy\autodeploy-server.exe'
# Ctrl+C to stop. Then Start-Service autodeploy to return to normal.
```

The server emits structured JSON logs to stdout. In service mode
those go to the SCM's stderr stream, which Windows discards by
default; for centralised log collection use the portal's log viewer
(Logs page) — every operator-relevant event is mirrored into the
database and surfaced through the UI. Server-host-level logging
(crashes, service starts) appears in Event Viewer under **Windows
Logs → System** with provider **Service Control Manager**.

## Upgrading

To upgrade in place:

```powershell
# 1. Download the new binary alongside the old one.
Invoke-WebRequest "$URL/autodeploy-server-windows-amd64.exe" -OutFile autodeploy-server-windows-amd64.exe

# 2. Re-run the installer. It stops the service, replaces the binary,
#    re-applies env, restarts.
.\scripts\windows\install-windows.ps1
```

The data directory and database are preserved across re-runs. The
env file at `C:\Program Files\AutoDeploy\autodeploy.env` is also
preserved (the installer never overwrites an existing env file).

## Backup

```powershell
.\scripts\windows\backup.ps1
```

Writes `C:\ProgramData\AutoDeploy\backups\autodeploy-<timestamp>.zip`
with the SQLite snapshot, the at-rest encryption key, TLS material
and the bootstrap-admin file if it's still there. Payload blobs
(ISOs, drivers, software) are NOT in the archive — take a separate
file copy if you need them.

**Restore**: stop the service, extract the archive into
`C:\ProgramData\AutoDeploy\`, start the service. Without
`secrets-key.bin` the escrowed PINs and recovery keys are
unreadable, so back this file up out-of-band.

## Uninstall

```powershell
.\scripts\windows\uninstall-windows.ps1
```

Removes the service, the firewall rules and the install directory.
The data directory is preserved by default; add `-PurgeData` to
delete it too.

## Caveats

- **TFTP on UDP 69 requires Administrator** to bind. Running the
  service as LocalSystem (the installer default) handles this. If
  you switch to NetworkService, move TFTP to an unprivileged port
  (e.g. `AUTODEPLOY_TFTP_ADDR=:6969`) and configure your DHCP option
  67 accordingly.
- **Defender / EDR** may quarantine the binary on first run because
  it's an unsigned exe. Add `C:\Program Files\AutoDeploy\` as an
  exclusion or sign the binary internally.
- **Windows updates** that require a reboot will restart the service
  automatically (it's set to start automatically). In-flight payload
  downloads are interrupted; the Boot Client retries with backoff.

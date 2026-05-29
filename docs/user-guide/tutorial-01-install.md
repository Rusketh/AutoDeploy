# Tutorial 1 — Install AutoDeploy

15 minutes. Gets the server running and the portal reachable. No
PXE configuration yet — that's [tutorial 2](tutorial-02-pxe.md).

## What you'll need

| | |
|---|---|
| **A host** | Linux (preferred), Windows, or macOS. 2 vCPU, 4 GB RAM, 100 GB disk to start. The disk grows with your ISO/driver library — a single Windows 11 ISO is ~5 GB. |
| **Root / sudo** (Linux) or **elevated PowerShell** (Windows) | The installers create a service user and bind to privileged ports. |
| **A LAN IP or DNS name** | Target machines need to reach the server. A local IP like `192.168.1.5` is fine for homelab; a DNS name is nicer if you can spare one. |

You **don't** need TLS to start. AutoDeploy is happy on plain HTTP
out of the box; you can add HTTPS later.

## Step 1 — Download the release

Visit
[github.com/Rusketh/AutoDeploy/releases](https://github.com/Rusketh/AutoDeploy/releases),
grab the latest `v*` tag.

For a Linux server:

```sh
TAG=v0.1.10        # whatever the latest tag is
URL=https://github.com/Rusketh/AutoDeploy/releases/download/$TAG
mkdir -p ~/autodeploy && cd ~/autodeploy

curl -LO $URL/autodeploy-server-linux-amd64
curl -LO $URL/autodeploy-server-linux-amd64.sha256
sha256sum -c autodeploy-server-linux-amd64.sha256

curl -LO $URL/autodeploy-extras.tar.gz
tar xzf autodeploy-extras.tar.gz   # → scripts/ docs/
```

For a Windows server, swap `linux-amd64` for `windows-amd64.exe`
and use `Invoke-WebRequest` instead of `curl`.

The other binaries (`autodeploy-boot-linux-amd64`,
`autodeploy-agent-windows-amd64.exe`) will be fetched
automatically by the install script if your host has internet
access. If you're installing offline, download them by hand and
drop them in the same directory.

## Step 2 — Run the installer

### Linux

```sh
sudo ./scripts/install-linux.sh
```

Output:

```
== Installing autodeploy-server ==
== Creating service account 'autodeploy' ==
== Creating /var/lib/autodeploy ==
== Installing systemd unit ==
== Installing /etc/default/autodeploy (edit me) ==
== Fetching iPXE bootstrap binaries ==
== Fetching autodeploy-agent-windows-amd64.exe from v0.1.10 ==
    fetched .../downloads/autodeploy-agent-windows-amd64.exe (sha256 verified)
== Installing self-update helper ==
    installed /etc/sudoers.d/autodeploy-update
============================================================
AutoDeploy is installed. Next steps: …
============================================================
```

What it did:

| File / location | Purpose |
|---|---|
| `/usr/local/bin/autodeploy-server` | The server binary. |
| `/usr/local/sbin/autodeploy-update` | In-place upgrade helper triggered by the portal's "Update" button. |
| `/etc/sudoers.d/autodeploy-update` | Narrowly-scoped sudo rule so the service user can launch the update helper. |
| `/etc/systemd/system/autodeploy.service` | systemd unit. |
| `/etc/default/autodeploy` | Environment file you'll edit in step 3. |
| `/var/lib/autodeploy/` | Data directory: SQLite, payload blobs, secrets key, downloads. |
| `/var/lib/autodeploy/ipxe/` | iPXE bootstrap binaries (`undionly.kpxe`, `ipxe.efi`, …). |
| `/var/lib/autodeploy/downloads/` | The Windows agent, Boot Client, etc., available via the portal Downloads page. |

### Windows

```powershell
.\scripts\windows\install-windows.ps1
```

Equivalent layout under `C:\Program Files\AutoDeploy\` and
`C:\ProgramData\AutoDeploy\`. Registers a native Windows Service
called `autodeploy`. See [Windows install
notes](install-windows.md) for the long-form walkthrough.

## Step 3 — Pick a TLS shape

Open `/etc/default/autodeploy` (Linux) or the env file on Windows.

Three shapes, in increasing order of work:

### Shape A — HTTP only (simplest)

Out of the box. Reach the portal at
`http://<this-host>:8080/portal/`.

```sh
AUTODEPLOY_HTTP_ADDR=0.0.0.0:8080
AUTODEPLOY_HTTPS_ADDR=
```

The server logs one `http.cleartext_public_bind` WARN at startup
when bound non-loopback in production mode so the choice is
auditable; nothing else changes.

Use this when your homelab is on a trusted LAN, or when a reverse
proxy (Caddy, nginx, IIS) handles TLS upstream.

### Shape B — HTTPS with a self-signed cert (quick lab TLS)

```sh
AUTODEPLOY_HTTP_ADDR=
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443
AUTODEPLOY_DEV=true       # makes the server auto-generate a self-signed cert
```

The server writes the cert to `$DATA_DIR/tls/dev-cert.pem` on
first start. Browsers warn; click through.

### Shape C — HTTPS with a real cert (production)

```sh
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443
AUTODEPLOY_TLS_CERT=/etc/autodeploy/tls/server.crt
AUTODEPLOY_TLS_KEY=/etc/autodeploy/tls/server.key
AUTODEPLOY_DEV=false
```

Your CA-signed cert. The cert and key files must be readable by
the `autodeploy` user (the install script does this).

If you set `HTTPS_ADDR` but forget the cert paths, the server
auto-generates a self-signed one and logs a WARN saying clients
won't trust it — you get a working listener while you sort out
the real cert.

## Step 4 — Set a secrets key

Still in the env file:

```sh
# Generate once and keep a copy off-host (password manager, vault).
AUTODEPLOY_SECRETS_KEY=$(openssl rand -hex 32)
```

This key encrypts BitLocker recovery keys, escrowed PINs, the AD
bind password — every secret AutoDeploy stores. **Lose it and
they're unrecoverable.** Back it up before going to production.

Leave it empty for now if you're just trying things out; the
server auto-generates a key file under
`$DATA_DIR/secrets-key.bin`.

## Step 5 — Start the service

```sh
sudo systemctl enable --now autodeploy
sudo systemctl status autodeploy
```

Expected log lines (`journalctl -u autodeploy -n 30 --no-pager`):

```
server.version version=v0.1.10
server.start  http_addr=0.0.0.0:8080 https_addr= dev_mode=false
http.listen   addr=0.0.0.0:8080
tftp.listen   addr=:69 root=/var/lib/autodeploy/ipxe
```

## Step 6 — Read the bootstrap admin password

The first start writes a one-time password to disk:

```sh
sudo cat /var/lib/autodeploy/admin-bootstrap.txt
# username: admin
# password: bHjVQPyGOXyy_FJ1-8M8Ug
```

Browse to `http://<this-host>:8080/portal/` (or `https://` if you
chose Shape B or C). Sign in as `admin` with that password.

You'll see a red banner: "**Bootstrap credentials still on disk.**"
Make it go away:

1. **Settings → Local accounts** → change the admin password.
2. `sudo rm /var/lib/autodeploy/admin-bootstrap.txt`

Refresh; the banner disappears.

## What's next

The portal is up but the server has no work to do yet. Make a
machine boot to it: → [Tutorial 2 — Set up PXE
boot](tutorial-02-pxe.md).

## If anything went wrong

See [Troubleshooting](troubleshooting.md). Common ones:

| Symptom | Fix |
|---|---|
| `permission denied binding to :443` | Unit needs `AmbientCapabilities=CAP_NET_BIND_SERVICE`. The install script sets it; if your distro ignores the directive, bind to a high port (`:8443`). |
| Browser opens `https://host:8080/` instead of `http://` | Most browsers auto-upgrade HTTP. Test with `curl -v http://<host>:8080/portal/` first. Disable "Always use secure connections" for the host. |
| `AUTODEPLOY_TLS_CERT and AUTODEPLOY_TLS_KEY must be set in production mode` | You set `HTTPS_ADDR` but no cert. Either fill in the paths, or comment out `HTTPS_ADDR`, or set `AUTODEPLOY_DEV=true` for a self-signed dev cert. |

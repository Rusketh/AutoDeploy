# Installation (Linux)

> First-time install? Read **[Getting started](getting-started.md)**
> for the full walkthrough. This page is the reference card.

For Windows, see [Windows installation](install-windows.md).

## Recommended path — Linux from GitHub release

```sh
TAG=v0.1.0
URL=https://github.com/Rusketh/AutoDeploy/releases/download/$TAG

curl -LO $URL/autodeploy-server-linux-amd64
curl -LO $URL/autodeploy-extras.tar.gz
tar xzf autodeploy-extras.tar.gz

sudo ./scripts/install-linux.sh
sudo systemctl enable --now autodeploy

# One-time bootstrap password
sudo cat /var/lib/autodeploy/admin-bootstrap.txt
```

The installer:

1. Copies the binary to `/usr/local/bin/autodeploy-server`.
2. Creates the `autodeploy` system user and `/var/lib/autodeploy/`
   data directory (with `ipxe/` and `downloads/` subdirectories).
3. Installs the systemd unit at
   `/etc/systemd/system/autodeploy.service`.
4. Installs `/etc/default/autodeploy` from the example env file
   (only if it doesn't already exist).
5. **Fetches the iPXE bootstrap binaries** (`undionly.kpxe`,
   `ipxe.efi`, `snponly.efi`, `ipxe.pxe`, `ipxe-arm64.efi`) from
   `boot.ipxe.org` into `/var/lib/autodeploy/ipxe/`. Pass
   `--no-ipxe` to skip if the install host has no internet access;
   re-run `scripts/fetch-ipxe.sh` later.
6. Seeds `/var/lib/autodeploy/downloads/` with any agent / Boot
   Client binaries that travelled in the release bundle, so the
   portal's Downloads page works out of the box.

## Building from source

Each component is a separate Go module under `server/`,
`boot-client/` and `agent/`. Each has a `Makefile` with `build`,
`test`, `vet` and `clean` targets.

Prerequisites: Go 1.22 or newer, GNU `make`.

```sh
# Server (host OS, by default).
make -C server build

# Boot Client (always linux/amd64 static for the initramfs).
make -C boot-client build

# Agent (host build, plus a windows/amd64 EXE).
make -C agent build         # autodeploy-agent on the host
make -C agent build-windows # autodeploy-agent.exe for deployed Windows machines
```

The CI workflow in `.github/workflows/ci.yml` runs the same `vet`,
`test` and `build` targets on every push and uploads the resulting
binaries as build artifacts.

## Manual / lab run (no systemd)

```sh
AUTODEPLOY_DATA_DIR=./data ./server/bin/autodeploy-server
```

Server logs to stdout; browse to `http://127.0.0.1:8080/portal/`.
The one-time password is in `./data/admin-bootstrap.txt`.

## Environment variables

See [Configuring the server](configuration.md) for the full
list. Defaults at a glance:

| Variable | Default | Meaning |
|---|---|---|
| `AUTODEPLOY_HTTP_ADDR` | `127.0.0.1:8080` | HTTP bind. Loopback by default. |
| `AUTODEPLOY_HTTPS_ADDR` | empty | HTTPS bind. Empty = no HTTPS listener. |
| `AUTODEPLOY_TLS_CERT` / `_KEY` | empty | PEM cert + key. Empty in dev mode auto-generates a self-signed cert. |
| `AUTODEPLOY_TFTP_ADDR` | empty | UDP TFTP listener for iPXE bootstrap. `:69` enables. |
| `AUTODEPLOY_DATA_DIR` | `./data` | Persistent state root. |
| `AUTODEPLOY_DEV` | `true` | `false` in production. |
| `AUTODEPLOY_SECRETS_KEY` | empty | Hex-encoded 32-byte at-rest encryption key. Empty auto-generates one. |

## On-disk layout

See [Configuring the server → On-disk layout](configuration.md#on-disk-layout)
for the full tree. Important paths:

| Path | What |
|---|---|
| `$DATA_DIR/autodeploy.sqlite` | The relational store. |
| `$DATA_DIR/secrets-key.bin` | AES-256-GCM key for at-rest encryption (mode 0600). Back this up out-of-band. |
| `$DATA_DIR/admin-bootstrap.txt` | One-time bootstrap admin password. Delete after first login. |
| `$DATA_DIR/ipxe/` | iPXE bootstrap binaries + Boot Client kernel/initrd. Served via the built-in TFTP listener. |
| `$DATA_DIR/downloads/` | Distributable binaries the portal Downloads page serves (agent, installers). |
| `$DATA_DIR/iso/<id>/`, `drivers/<id>/`, `software/<id>/` | Per-row payload trees. |

## Health check

```sh
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

## Verifying the install

1. `systemctl status autodeploy` — should be active (running).
2. Browse to `http://<host>:8080/portal/` (or your HTTPS URL).
3. Sign in with the bootstrap password.
4. Change the password under **Settings → Local accounts**.
5. Delete `/var/lib/autodeploy/admin-bootstrap.txt`.
6. The red banner at the top of the portal goes away.

## Upgrading

```sh
# Download the new binary
curl -LO $URL/autodeploy-server-linux-amd64

# Re-run the installer. It stops the service, replaces the binary,
# preserves the data dir and the env file, restarts.
sudo ./scripts/install-linux.sh
```

The installer is idempotent. The systemd unit and
`/etc/default/autodeploy` are preserved if they already exist;
override with the `--data` and other flags.

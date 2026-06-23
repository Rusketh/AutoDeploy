# AutoDeploy

**A network-driven Windows deployment and configuration platform.**
AutoDeploy network-boots bare-metal or virtual machines, installs Windows from your own media,
applies drivers and software, and then keeps each machine under management through a resident
agent — all from a single web portal.

There is no client to pre-install: a machine PXE-boots, appears in the portal, and you deploy to it.

```
   PXE boot ──► Boot Client (Linux)        Server (portal + API)        Agent (Windows)
   the machine   stages Windows media   ◄──►  decides what to deploy  ◄──►  installs software,
   network-boots  onto the local disk         and records every fact        applies config,
   into a menu                                                               stays resident
```

## What you can do with it

- **Network-boot and image** machines over PXE / iPXE using your own Windows ISOs.
- **Answer-file (unattend) automation** — locale, accounts, OOBE, domain join.
- **Driver packages** matched to hardware automatically from SMBIOS identity.
- **Software packages & loadouts** with detection rules and scripted install steps.
- **Inventory** of every machine — hardware, bindings, and deployment history.
- **Bulk operations** — reimage, rename, run scripts, or push software to many machines at once.
- **Active Directory** domain join and lookups.
- **Payload mirrors** for scaling across sites, and **audit logging** of every action.

## Architecture at a glance

Three Go programs that talk over HTTP(S):

| Component | Binary | Runs on | Role |
|-----------|--------|---------|------|
| **Server** | `autodeploy-server` | Linux | Web portal, JSON API, orchestration, SQLite store, optional built-in TFTP. The single source of truth. |
| **Boot Client** | `autodeploy-boot` | Linux (initramfs, via iPXE) | Pre-OS imaging: reads hardware identity, stages Windows media onto the local disk, reboots. |
| **Agent** | `autodeploy-agent` | Windows | Post-install configuration and a resident service that polls the server for work. |

The server **decides**; the boot client and agent **report facts and fail safe**.

---

## Quick start

> The **server runs on Linux**. The Windows agent is delivered to your machines automatically —
> you never install the server on Windows.

### 1. Install

```bash
# Resolve the latest release, download the server + extras bundle, and run the installer
TAG=$(curl -fsSL https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
  | grep -oP '"tag_name":\s*"\K[^"]+')
BASE="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG"
curl -fLO "$BASE/autodeploy-server-linux-amd64"     # use -arm64 on aarch64
curl -fLO "$BASE/autodeploy-extras.tar.gz"

tar xzf autodeploy-extras.tar.gz
sudo ./scripts/install-linux.sh
sudo systemctl enable --now autodeploy
```

On first start the server writes a one-time admin password to
`/var/lib/autodeploy/admin-bootstrap.txt`. Read it, open **`http://<server>:8080/portal/`**, log in
as `admin`, change the password, then delete that file.

Full steps (including pinning a version and air-gapped installs):
[docs/install/linux-server.md](docs/install/linux-server.md).

### 2. Configure

Settings live in `/etc/default/autodeploy` (run `sudo systemctl restart autodeploy` after editing).
The defaults serve **HTTP on `:8080`** with the built-in **TFTP** server on `:69`:

```ini
AUTODEPLOY_HTTP_ADDR=0.0.0.0:8080
#AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443     # uncomment to enable HTTPS
AUTODEPLOY_TFTP_ADDR=:69
#AUTODEPLOY_SECRETS_KEY=               # openssl rand -hex 32  — then back it up
```

- **HTTPS** — uncomment `AUTODEPLOY_HTTPS_ADDR`. Add `AUTODEPLOY_TLS_CERT` / `AUTODEPLOY_TLS_KEY`
  for a trusted certificate, leave them blank to start with a self-signed one, or terminate TLS at a
  reverse proxy. HTTP-only is a valid production shape.
- **Secrets at rest** — set `AUTODEPLOY_SECRETS_KEY` (or let the server generate
  `<data>/secrets-key.bin`) and **back it up**; it encrypts domain-join and webhook secrets.

Active Directory, notifications, branding, storage paths and retention are all configured in the
portal. Every variable: [docs/reference/configuration.md](docs/reference/configuration.md).

### 3. Network (PXE & DNS)

- Give the server a **stable IP or DNS name** (e.g. `deploy.example.com`) that resolves from the
  deployment VLAN — clients use it for the whole deployment.
- Point DHCP at AutoDeploy's iPXE bootstrap. The simplest path uses the **built-in TFTP** server:
  set DHCP `next-server` to the AutoDeploy host and the boot filename to the iPXE binary for the
  client firmware (`undionly.kpxe` for legacy BIOS, `ipxe.efi` for UEFI). iPXE then chainloads
  `http://<server>:8080/ipxe/boot.ipxe` on its own.
- Already running iPXE or your own TFTP server? Just chainload `…/ipxe/boot.ipxe` and leave
  `AUTODEPLOY_TFTP_ADDR` empty.

DHCP/iPXE examples and UEFI Secure Boot: [docs/install/pxe-and-boot.md](docs/install/pxe-and-boot.md).

Then deploy your first machine end to end: [docs/getting-started.md](docs/getting-started.md).

---

## Documentation

The complete, screenshot-rich user guide lives in **[docs/](docs/README.md)**:

- **[Introduction](docs/introduction.md)** & **[Concepts](docs/concepts.md)** — how it all fits together.
- **[Getting started](docs/getting-started.md)** — your first end-to-end deployment.
- **Install** — [Linux server](docs/install/linux-server.md) & [PXE / boot setup](docs/install/pxe-and-boot.md).
- **Portal** — [dashboard](docs/portal/dashboard.md), [payloads](docs/portal/payloads.md),
  [software](docs/portal/software.md), [images](docs/portal/images.md),
  [machines](docs/portal/machines.md), [bulk operations](docs/portal/bulk-operations.md),
  [mirrors](docs/portal/mirrors.md), [settings](docs/portal/settings.md), and more.
- **Reference** — [configuration](docs/reference/configuration.md), [CLI](docs/reference/cli.md),
  [detection rules](docs/reference/detection-rules.md), [install steps](docs/reference/install-steps.md),
  [JSON API](docs/reference/api.md).
- **Operations** — [security](docs/operations/security.md),
  [Active Directory](docs/operations/active-directory.md), [scaling](docs/operations/scaling.md),
  [backup & retention](docs/operations/backup-and-retention.md), [updates](docs/operations/updates.md),
  [troubleshooting](docs/operations/troubleshooting.md).

## Building from source

All three components are Go modules (Go 1.25+):

```bash
cd server      && go build ./cmd/autodeploy-server
cd boot-client && go build ./cmd/autodeploy-boot
cd agent       && go build ./cmd/autodeploy-agent
```

Run the test suites with `go test ./...` in each module.

## Repository layout

```
server/        AutoDeploy server (portal, API, orchestration)
boot-client/   Pre-OS Linux imaging client
agent/         Windows in-OS / resident agent
credprovider/  Windows setup-lock credential provider (C++)
scripts/       Install, iPXE, initramfs, backup and update helpers
docs/          User documentation (this guide) and design notes
```

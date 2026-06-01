# AutoDeploy

**AutoDeploy is a network-driven Windows deployment and configuration platform.**
It boots bare-metal or virtual machines over the network, installs Windows from your own
media, applies drivers and software, and then keeps each machine under management through a
resident agent — all from a single web portal.

Everything is driven over HTTP(S). There is no client to pre-install: a machine PXE-boots,
shows up in the portal, and you deploy to it.

```
   PXE boot ──► Boot Client (Linux)        Server (portal + API)        Agent (Windows)
   the machine   stages Windows media   ◄──►  decides what to deploy  ◄──►  installs software,
   network-boots  onto the local disk         and records every fact        enables BitLocker,
   into a menu                                                               stays resident
```

---

## What you can do with it

- **Network-boot and image machines** over PXE / iPXE using your own Windows ISOs.
- **Answer-file (unattend) automation** — locale, accounts, OOBE, domain join.
- **Driver packages** matched to hardware automatically from SMBIOS identity.
- **Software packages & loadouts** with detection rules and scripted install steps.
- **Inventory** of every machine, its hardware, bindings, and deployment history.
- **Bulk operations** — reimage, rename, run scripts, or push software to many machines at once.
- **BitLocker** enablement with recovery-key escrow.
- **Active Directory** domain join and lookups.
- **Payload mirrors** for scaling deployments across sites.
- **Audit logging** of every operator and client action.

## Architecture at a glance

AutoDeploy is three Go programs that talk to each other over HTTP(S):

| Component | Binary | Runs on | Role |
|-----------|--------|---------|------|
| **Server** | `autodeploy-server` | Linux | Web portal, JSON API, deployment orchestration, SQLite store, optional built-in TFTP. The single source of truth. |
| **Boot Client** | `autodeploy-boot` | Linux (in initramfs, via iPXE) | Pre-OS imaging: reads hardware identity, fetches the deployment manifest, stages Windows media onto disk, reboots. |
| **Agent** | `autodeploy-agent` | Windows | Post-install configuration and a resident service that polls the server for work (software, rename, reimage, BitLocker). |

The server **decides**; the boot client and agent **report facts and fail safe**.

## Quick start (Linux server)

> The AutoDeploy **server runs on Linux**. The Windows **agent** is delivered automatically to
> the machines you deploy — you don't install the server on Windows.

Download the **latest release**, then run the installer:

```bash
# Resolve the latest release tag from GitHub
TAG=$(curl -fsSL https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
  | grep -oP '"tag_name":\s*"\K[^"]+')
echo "Latest release: $TAG"

# Download the server binary (amd64) + the extras bundle (install script, systemd unit, iPXE helper)
curl -fLO "https://github.com/Rusketh/AutoDeploy/releases/download/$TAG/autodeploy-server-linux-amd64"
curl -fLO "https://github.com/Rusketh/AutoDeploy/releases/download/$TAG/autodeploy-extras.tar.gz"

# Unpack the extras and run the installer (expects the server binary alongside it)
tar xzf autodeploy-extras.tar.gz
sudo ./scripts/install-linux.sh

# Start the service
sudo systemctl enable --now autodeploy
```

On first start the server writes a one-time admin password to
`/var/lib/autodeploy/admin-bootstrap.txt`. Read it, open `https://<server>/portal/`, log in as
`admin`, change the password, then delete that file.

Full, step-by-step instructions (including how to always fetch the latest version) are in
**[docs/install/linux-server.md](docs/install/linux-server.md)**.

## Documentation

The complete, screenshot-rich user guide lives in **[docs/](docs/README.md)**:

- **[Introduction](docs/introduction.md)** & **[Concepts](docs/concepts.md)** — how it all fits together.
- **[Getting started](docs/getting-started.md)** — your first end-to-end deployment.
- **[Installation](docs/install/linux-server.md)** & **[PXE / boot setup](docs/install/pxe-and-boot.md)**.
- **Portal guide** — [dashboard](docs/portal/dashboard.md), [payloads](docs/portal/payloads.md),
  [software](docs/portal/software.md), [images](docs/portal/images.md),
  [machines](docs/portal/machines.md), [bulk operations](docs/portal/bulk-operations.md),
  [mirrors](docs/portal/mirrors.md), [logs](docs/portal/logs.md),
  [downloads](docs/portal/downloads.md), [settings](docs/portal/settings.md).
- **Reference** — [configuration](docs/reference/configuration.md), [CLI](docs/reference/cli.md),
  [detection rules](docs/reference/detection-rules.md), [install steps](docs/reference/install-steps.md),
  [JSON API](docs/reference/api.md).
- **Operations** — [security](docs/operations/security.md),
  [Active Directory](docs/operations/active-directory.md), [BitLocker](docs/operations/bitlocker.md),
  [scaling](docs/operations/scaling.md), [backup & retention](docs/operations/backup-and-retention.md),
  [updates](docs/operations/updates.md), [troubleshooting](docs/operations/troubleshooting.md).

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
scripts/       Install, iPXE, initramfs, backup and update helpers
docs/          User documentation (this guide) and design notes
```
</content>

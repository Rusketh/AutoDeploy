# AutoDeploy

A remote operating-system deployment and configuration platform for Windows
machines. A single replacement for WDS, MDT, SCCM and FOG, delivered entirely
over HTTP(S) and driven from a web portal.

## Get started

**New operator?** Read
**[`docs/user-guide/getting-started.md`](docs/user-guide/getting-started.md)** —
zero to your first deployed machine in 30–60 minutes.

**Downloading a release?** Grab the binaries for your platform from
the [Releases page](https://github.com/Rusketh/AutoDeploy/releases).
Each tag publishes:

| File pattern                                   | Component                                                                  |
|------------------------------------------------|----------------------------------------------------------------------------|
| `autodeploy-server-<os>-<arch>`                | Management portal + JSON API + payload + AD service. Place on `$PATH`.    |
| `autodeploy-boot-linux-<arch>`                 | Linux Boot Client. Goes into the initramfs (see `build-initramfs.sh`).    |
| `autodeploy-agent-<os>-<arch>`                 | In-OS Deployment Client. Bundled into Windows images by the unattend.     |
| `autodeploy-extras.tar.gz`                     | Operator scripts (install, fetch-ipxe, build-initramfs, backup, systemd unit) and the full user guide for the release. |
| `*.sha256`                                     | Download verification.                                                     |

The fastest path on Linux is:

```sh
# 1. Download from the Releases page (substitute the right tag):
TAG=v0.1.0
URL=https://github.com/Rusketh/AutoDeploy/releases/download/$TAG
curl -LO $URL/autodeploy-server-linux-amd64
curl -LO $URL/autodeploy-extras.tar.gz
tar xzf autodeploy-extras.tar.gz

# 2. Install (system user + systemd unit + iPXE bootstrap binaries):
sudo ./scripts/install-linux.sh

# 3. Edit /etc/default/autodeploy, then:
sudo systemctl enable --now autodeploy

# 4. Read the bootstrap admin password and change it in the portal:
sudo cat /var/lib/autodeploy/admin-bootstrap.txt
# → https://your-server/portal/  (Settings → Local accounts)
```

Everything beyond this — building the initramfs, configuring DHCP,
uploading ISOs, composing images, deploying machines — is in
[`getting-started.md`](docs/user-guide/getting-started.md).

## What's in the repository

| Path           | Component                                                   |
|----------------|-------------------------------------------------------------|
| `server/`      | Management portal, HTTP API, Deployment Service, Domain Integration Service, TFTP (Go) |
| `boot-client/` | Linux pre-OS imaging client, chainloaded over HTTP via iPXE (Go) |
| `agent/`       | In-OS resident Deployment Client for Windows (Go)           |
| `scripts/`     | Install, build, iPXE, initramfs, backup helpers + systemd unit |
| `docs/`        | Design documents, worklog and operator guide                |

## Building from source

Each component is a separate Go module and is built independently. CI
(GitHub Actions, `.github/workflows/ci.yml`) builds and tests all three
on every push.

```sh
make -C server build
make -C boot-client build
make -C agent build
```

Tagged releases are built and published automatically by
`.github/workflows/release.yml` when a `v*` tag is pushed.

## Documentation

| Document                                                           | Purpose                                                       |
|--------------------------------------------------------------------|----------------------------------------------------------------|
| [Getting started](docs/user-guide/getting-started.md)              | The 30–60 minute first-install tutorial.                       |
| [Operator guide](docs/user-guide/README.md)                        | Index of all reference documentation by topic.                  |
| [Operations / production](docs/user-guide/operations.md)           | Topology, backup, retention, security review.                   |
| [Scaling](docs/user-guide/scaling.md)                              | Payload mirrors, throttling, metrics.                            |
| [PXE setup](docs/user-guide/pxe-setup.md)                          | DHCP / TFTP / iPXE for classic PXE and UEFI HTTP Boot.          |
| [Design document](docs/design/AutoDeploy_Design_Document.docx)     | The authoritative architecture spec.                            |
| [Development worklog](docs/design/WORKLOG.md)                      | Running log of what was built and why.                         |

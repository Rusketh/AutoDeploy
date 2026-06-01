# Installing the AutoDeploy server (Linux)

The AutoDeploy **server runs on Linux**. This page installs it as a systemd service from an
official release. You do **not** install the server on Windows — the Windows
[agent](../introduction.md#agent-autodeploy-agent) is delivered to target machines automatically
during deployment.

## Requirements

- A 64-bit Linux host (**x86-64 / amd64** or **ARM64 / aarch64**) with `systemd`.
- `root` (sudo) access.
- Outbound HTTPS to GitHub (to download the release), or a way to copy the files in for
  air-gapped installs.

The server is a single statically-linked binary with no runtime dependencies. Two optional
command-line tools make ISO handling smoother and are installed automatically when available:

- **p7zip** (`7z`/`7za`) or `bsdtar` — to extract Windows ISOs (modern Windows ISOs are UDF).
- **wimlib** (`wimlib-imagex`, packaged as `wimtools`/`wimlib-utils`) — to split an `install.wim`
  larger than FAT32's 4 GiB per-file limit.

If a package can't be installed automatically the installer prints a warning and continues; the
portal later shows a clear message if it needs one of these tools.

## Step 1 — Get the latest release

Releases are published on GitHub at **<https://github.com/Rusketh/AutoDeploy/releases>**. The
newest one is always at **<https://github.com/Rusketh/AutoDeploy/releases/latest>**.

The reliable way to fetch the latest version is to ask GitHub's API for the latest release tag and
download the matching assets. Copy-paste this:

```bash
# 1. Resolve the latest release tag (e.g. v0.1.52)
TAG=$(curl -fsSL https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
  | grep -oP '"tag_name":\s*"\K[^"]+')
echo "Latest AutoDeploy release: $TAG"

# 2. Pick the binary for your CPU architecture
case "$(uname -m)" in
  x86_64)  ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

# 3. Download the server binary, its checksum, and the extras bundle
BASE="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG"
curl -fLO "$BASE/autodeploy-server-linux-$ARCH"
curl -fLO "$BASE/autodeploy-server-linux-$ARCH.sha256"
curl -fLO "$BASE/autodeploy-extras.tar.gz"
```

> **Want to pin a specific version instead?** Set `TAG` yourself, e.g. `TAG=v0.1.52`, and skip
> the API call. You can see every available tag on the
> [releases page](https://github.com/Rusketh/AutoDeploy/releases).

### Verify the download (recommended)

Each binary ships with a `.sha256` sidecar. Confirm the download is intact:

```bash
sha256sum -c "autodeploy-server-linux-$ARCH.sha256"
```

It should print `OK`.

## Step 2 — Unpack the extras bundle

`autodeploy-extras.tar.gz` contains the installer (`scripts/install-linux.sh`), the systemd unit,
the iPXE fetch helper, and other operational scripts:

```bash
tar xzf autodeploy-extras.tar.gz
```

The installer expects the server binary to sit next to the `scripts/` directory — which it does if
you ran the commands above in the same folder.

## Step 3 — Run the installer

```bash
sudo ./scripts/install-linux.sh
```

The installer:

- copies the binary to `/usr/local/bin/autodeploy-server`,
- best-effort installs the optional `p7zip` / `wimtools` packages,
- creates the `autodeploy` system user and the data directory `/var/lib/autodeploy`
  (with `ipxe/` and `downloads/` subdirectories),
- seeds bundled agent/boot binaries and fetches any missing ones for **all available platforms**
  from the release that matches the installed server version,
- installs the self-update helper at `/usr/local/sbin/autodeploy-update` and a sudoers rule at
  `/etc/sudoers.d/autodeploy` so the server can trigger in-place updates from the portal (see
  [Updates](../operations/updates.md)),
- installs the systemd unit at `/etc/systemd/system/autodeploy.service` and the environment file
  at `/etc/default/autodeploy`,
- fetches the stock iPXE bootstrap binaries (unless you pass `--no-ipxe`).

### Installer options

| Flag | Purpose |
|------|---------|
| `--binary PATH` | Use a specific server binary instead of auto-detecting by architecture. |
| `--data DIR` | Use a data directory other than `/var/lib/autodeploy`. |
| `--no-ipxe` | Skip downloading the iPXE bootstrap binaries. |
| `-h`, `--help` | Print usage and exit. |

## Step 4 — Configure

The server reads its configuration from the environment file `/etc/default/autodeploy`. The
defaults bind the portal on ports 80 (HTTP) and 443 (HTTPS) and enable the built-in TFTP server on
port 69:

```ini
AUTODEPLOY_HTTP_ADDR=0.0.0.0:80
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443
AUTODEPLOY_TLS_CERT=
AUTODEPLOY_TLS_KEY=
AUTODEPLOY_DATA_DIR=/var/lib/autodeploy
AUTODEPLOY_TFTP_ADDR=:69
```

For a production HTTPS listener, set `AUTODEPLOY_TLS_CERT` and `AUTODEPLOY_TLS_KEY` to your
certificate and key (both are required when HTTPS is enabled outside dev mode). The full list of
settings is in the [configuration reference](../reference/configuration.md).

> The systemd unit grants the binary `CAP_NET_BIND_SERVICE`, so it can bind ports 80/443/69 while
> running as the unprivileged `autodeploy` user.

## Step 5 — Start the service

```bash
sudo systemctl enable --now autodeploy
sudo systemctl status autodeploy
```

Follow the logs with:

```bash
journalctl -u autodeploy -f
```

## Step 6 — First login

On first start, with no accounts yet, the server creates an `admin` account and writes a one-time
password to **`/var/lib/autodeploy/admin-bootstrap.txt`** (it also logs a warning pointing you to
the file):

```bash
sudo cat /var/lib/autodeploy/admin-bootstrap.txt
```

Open `https://<your-server>/portal/`, log in as `admin` with that password, then:

1. Change the password in **[Settings → Accounts](../portal/settings.md#accounts)**.
2. Delete the bootstrap file:
   ```bash
   sudo rm /var/lib/autodeploy/admin-bootstrap.txt
   ```

You're ready to set up [PXE booting](pxe-and-boot.md) and create your first
[image](../portal/images.md).

## Keeping the server up to date

To upgrade later, repeat **Step 1** to fetch the newest release and re-run the installer; it
upgrades the binary in place and preserves your data directory and database. The portal can also
manage updates for you — see [Updates](../operations/updates.md).

## Air-gapped installs

If the host has no internet access, download the release assets on a connected machine and copy
them across. Place the agent/boot binaries from the release next to `scripts/install-linux.sh`
before running it so the installer can seed them locally, and pass `--no-ipxe` (then provide iPXE
binaries manually under `<data>/ipxe/`).
</content>

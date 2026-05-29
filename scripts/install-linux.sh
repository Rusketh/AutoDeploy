#!/usr/bin/env bash
# scripts/install-linux.sh
#
# Install AutoDeploy on a Linux host: place the binary, create the
# system user and data directories, install a systemd unit, and
# (optionally) fetch the iPXE bootstrap binaries.
#
# Run as root.
#
# Usage:
#   ./install-linux.sh [--binary PATH] [--data DIR] [--no-ipxe]
#
# By default it expects autodeploy-server-linux-amd64 in the current
# directory (the file you downloaded from the GitHub release).
set -euo pipefail

BIN_SRC=""
DATA_DIR=/var/lib/autodeploy
FETCH_IPXE=1

while [ $# -gt 0 ]; do
    case "$1" in
        --binary)  BIN_SRC="$2"; shift 2 ;;
        --data)    DATA_DIR="$2"; shift 2 ;;
        --no-ipxe) FETCH_IPXE=0; shift ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//' | head -n -1
            exit 0 ;;
        *) echo "Unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [ "$EUID" -ne 0 ]; then
    echo "Must be run as root (sudo)." >&2
    exit 1
fi

# Auto-detect the downloaded binary by architecture if --binary wasn't given.
if [ -z "$BIN_SRC" ]; then
    arch="$(uname -m)"
    case "$arch" in
        x86_64)   BIN_SRC="$(pwd)/autodeploy-server-linux-amd64" ;;
        aarch64)  BIN_SRC="$(pwd)/autodeploy-server-linux-arm64" ;;
        *) echo "Unsupported architecture $arch; pass --binary PATH" >&2; exit 1 ;;
    esac
fi
if [ ! -f "$BIN_SRC" ]; then
    echo "Server binary not found at $BIN_SRC." >&2
    echo "Download from https://github.com/Rusketh/AutoDeploy/releases and pass --binary PATH." >&2
    exit 1
fi

echo "== Installing autodeploy-server =="
install -m 0755 "$BIN_SRC" /usr/local/bin/autodeploy-server

echo "== Creating service account 'autodeploy' =="
if ! id -u autodeploy >/dev/null 2>&1; then
    useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin autodeploy
fi

echo "== Creating $DATA_DIR =="
install -d -m 0750 -o autodeploy -g autodeploy "$DATA_DIR"
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/ipxe"
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/downloads"

# Seed the downloads directory with any agent / boot client binaries
# that travelled in the same release tarball, so the portal's
# Downloads page works out of the box.
HERE_PARENT="$(cd "$(dirname "$0")/.." && pwd)"
for candidate in autodeploy-agent-windows-amd64.exe autodeploy-agent-windows-arm64.exe \
                 autodeploy-agent-linux-amd64 \
                 autodeploy-boot-linux-amd64 autodeploy-boot-linux-arm64; do
    for src in "$HERE_PARENT/$candidate" "$(pwd)/$candidate"; do
        if [ -f "$src" ] && [ ! -f "$DATA_DIR/downloads/$candidate" ]; then
            install -m 0644 -o autodeploy -g autodeploy "$src" "$DATA_DIR/downloads/$candidate"
            echo "    seeded $DATA_DIR/downloads/$candidate"
            # Sidecar files written alongside by the release workflow.
            # The .version sidecar is what the server reads to decide
            # whether an agent should self-update; the .sha256 is what
            # the agent verifies before swapping its binary. Copy both
            # if they exist in the same release bundle.
            for sidecar in "$candidate.version" "$candidate.sha256"; do
                if [ -f "$src.${sidecar#$candidate.}" ]; then
                    install -m 0644 -o autodeploy -g autodeploy \
                        "$src.${sidecar#$candidate.}" \
                        "$DATA_DIR/downloads/$sidecar"
                fi
            done
            break
        fi
    done
done

# Install the systemd unit and example env file.
HERE="$(cd "$(dirname "$0")" && pwd)"
UNIT_SRC="$HERE/systemd/autodeploy.service"
ENV_SRC="$HERE/systemd/autodeploy.env.example"
if [ -f "$UNIT_SRC" ]; then
    echo "== Installing systemd unit =="
    install -m 0644 "$UNIT_SRC" /etc/systemd/system/autodeploy.service
fi
if [ -f "$ENV_SRC" ] && [ ! -f /etc/default/autodeploy ]; then
    echo "== Installing /etc/default/autodeploy (edit me) =="
    install -m 0640 -o root -g autodeploy "$ENV_SRC" /etc/default/autodeploy
fi

if [ "$FETCH_IPXE" -eq 1 ]; then
    if [ -x "$HERE/fetch-ipxe.sh" ]; then
        echo "== Fetching iPXE bootstrap binaries =="
        # Don't abort the installer if the network is unreachable --
        # the operator can re-run fetch-ipxe.sh later, or build the
        # iPXE binaries by hand and drop them in $DATA_DIR/ipxe.
        if ! AUTODEPLOY_DATA_DIR="$DATA_DIR" "$HERE/fetch-ipxe.sh"; then
            echo "  WARNING: iPXE fetch failed. Re-run scripts/fetch-ipxe.sh once you" >&2
            echo "  have network access, or drop the binaries into $DATA_DIR/ipxe by hand." >&2
        fi
        chown -R autodeploy:autodeploy "$DATA_DIR/ipxe"
    fi
fi

systemctl daemon-reload || true

cat <<EOF

============================================================
AutoDeploy is installed. Next steps:

  1. Review and edit /etc/default/autodeploy.
     Generate a secrets-at-rest key once:
         openssl rand -hex 32
     and paste it into AUTODEPLOY_SECRETS_KEY (or leave empty
     to auto-generate a key file under $DATA_DIR/secrets-key.bin).

  2. (Production) Place a TLS cert + key:
         /etc/autodeploy/tls/server.crt
         /etc/autodeploy/tls/server.key
     and point AUTODEPLOY_TLS_CERT / KEY at them.

  3. Start the service:
         systemctl enable --now autodeploy

  4. Read the bootstrap admin password (one-time):
         cat $DATA_DIR/admin-bootstrap.txt
     Log in at https://<this-host>/portal/ and change it via
     Settings → Local accounts. Delete the file when done.

  5. Drop a Linux kernel into $DATA_DIR/ipxe/autodeploy-kernel
     and an initramfs into $DATA_DIR/ipxe/autodeploy-initrd.
     Build the initramfs with scripts/initramfs/build-initramfs.sh.

  6. Configure DHCP to chainload undionly.kpxe (BIOS) or
     ipxe.efi (UEFI) — see docs/user-guide/pxe-setup.md.

Status: systemctl status autodeploy
Logs:   journalctl -u autodeploy -f
============================================================
EOF

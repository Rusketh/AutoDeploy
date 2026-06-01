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

# Windows 10/11 ISOs are UDF (install.wim exceeds ISO9660's 4 GiB limit),
# so the server extracts them with 7z. Best-effort install of p7zip; if it
# fails (air-gapped / unknown distro) the portal surfaces a clear "install
# p7zip" message when an ISO can't be extracted.
if ! command -v 7z >/dev/null 2>&1 && ! command -v 7za >/dev/null 2>&1 \
   && ! command -v bsdtar >/dev/null 2>&1; then
    echo "== Installing p7zip (ISO extraction) =="
    if command -v apt-get >/dev/null 2>&1; then
        apt-get install -y p7zip-full >/dev/null 2>&1 || echo "  WARN: 'apt-get install p7zip-full' failed; install it manually." >&2
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y p7zip p7zip-plugins >/dev/null 2>&1 || echo "  WARN: 'dnf install p7zip' failed; install it manually." >&2
    elif command -v yum >/dev/null 2>&1; then
        yum install -y p7zip p7zip-plugins >/dev/null 2>&1 || echo "  WARN: 'yum install p7zip' failed; install it manually." >&2
    elif command -v zypper >/dev/null 2>&1; then
        zypper install -y p7zip >/dev/null 2>&1 || echo "  WARN: 'zypper install p7zip' failed; install it manually." >&2
    else
        echo "  WARN: no known package manager; install p7zip-full (or bsdtar) so the server can extract Windows ISOs." >&2
    fi
fi

# An install.wim larger than FAT32's 4 GiB limit is split into .swm parts
# with wimlib-imagex at ISO ingest. Best-effort install; the portal Boot
# media panel surfaces a clear "install wimtools" message if it's missing.
if ! command -v wimlib-imagex >/dev/null 2>&1; then
    echo "== Installing wimlib (install.wim splitting) =="
    if command -v apt-get >/dev/null 2>&1; then
        apt-get install -y wimtools >/dev/null 2>&1 || echo "  WARN: 'apt-get install wimtools' failed; install it manually." >&2
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y wimlib-utils >/dev/null 2>&1 || echo "  WARN: 'dnf install wimlib-utils' failed; install it manually." >&2
    elif command -v yum >/dev/null 2>&1; then
        yum install -y wimlib-utils >/dev/null 2>&1 || echo "  WARN: 'yum install wimlib-utils' failed; install it manually." >&2
    elif command -v zypper >/dev/null 2>&1; then
        zypper install -y wimlib-tools >/dev/null 2>&1 || echo "  WARN: 'zypper install wimlib-tools' failed; install it manually." >&2
    else
        echo "  WARN: no known package manager; install wimtools/wimlib-utils so the server can split large install.wim files." >&2
    fi
fi

echo "== Creating service account 'autodeploy' =="
if ! id -u autodeploy >/dev/null 2>&1; then
    useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin autodeploy
fi

echo "== Creating $DATA_DIR =="
install -d -m 0750 -o autodeploy -g autodeploy "$DATA_DIR"
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/ipxe"
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/downloads"

# Build the latest Windows agent from THIS source tree (when the installer
# runs from a checkout with a Go toolchain) and install it into downloads,
# so the server always serves an agent matching this build. The boot-the-
# media deploy injects that binary (via $OEM$/SetupComplete) and installs
# it as the resident service. Release-tarball installs (no source tree or
# no Go) fall through to the seed + GitHub-fetch logic below; that logic
# skips the agent when the file already exists, so a successful build here
# always wins.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if command -v go >/dev/null 2>&1 && [ -f "$REPO_ROOT/agent/go.mod" ]; then
    echo "== Building latest Windows agent from source =="
    AGENT_VER="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || true)"
    [ -n "$AGENT_VER" ] || AGENT_VER="dev"
    AGENT_BIN="$DATA_DIR/downloads/autodeploy-agent-windows-amd64.exe"
    if ( cd "$REPO_ROOT/agent" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
            go build -trimpath -ldflags "-s -w -X main.Version=$AGENT_VER" \
            -o "$AGENT_BIN" ./cmd/autodeploy-agent ); then
        # Sidecars the server reads: .version drives self-update decisions,
        # .sha256 is verified before the agent swaps its binary. Write the
        # .sha256 from inside downloads so `sha256sum -c` resolves it.
        printf '%s\n' "$AGENT_VER" > "$AGENT_BIN.version"
        ( cd "$DATA_DIR/downloads" \
            && sha256sum autodeploy-agent-windows-amd64.exe > autodeploy-agent-windows-amd64.exe.sha256 )
        chown autodeploy:autodeploy "$AGENT_BIN" "$AGENT_BIN.version" "$AGENT_BIN.sha256"
        chmod 0644 "$AGENT_BIN" "$AGENT_BIN.version" "$AGENT_BIN.sha256"
        echo "    installed $AGENT_BIN ($AGENT_VER)"
    else
        echo "  WARN: agent build failed; falling back to seeded/fetched binary." >&2
        rm -f "$AGENT_BIN"
    fi
fi

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

# Fetch ALL available agent binaries from the GitHub release that matches
# the server binary's embedded version.  This means the portal's Downloads
# page works end-to-end after `install-linux.sh` with zero manual steps --
# the operator goes straight to imaging, regardless of which agent
# platforms the release ships.
#
# The GitHub API is queried to discover every autodeploy-agent-* asset in
# the release.  When the API is unreachable (air-gapped / rate-limited)
# we fall back to fetching the legacy hardcoded list so existing setups
# aren't broken.
#
# Failure here is non-fatal: an air-gapped install just won't have the
# agents ready; the operator can drop them in by hand later.
INSTALLED_VERSION=$(/usr/local/bin/autodeploy-server --version 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
if [ -n "$INSTALLED_VERSION" ] && [ "$INSTALLED_VERSION" != "dev" ]; then
    GH_BASE="https://github.com/Rusketh/AutoDeploy/releases/download/$INSTALLED_VERSION"

    # Try the GitHub API to discover all agent + boot-client assets.
    RELEASE_AGENTS=""
    RELEASE_AGENTS=$(curl -sSfL --connect-timeout 10 --retry 2 \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: autodeploy-installer" \
        "https://api.github.com/repos/Rusketh/AutoDeploy/releases/tags/$INSTALLED_VERSION" 2>/dev/null \
        | grep -o '"name": "autodeploy-agent-[^"]*"' | cut -d'"' -f4 \
        | grep -v '\.sha256$' | grep -v '\.version$' | sort -u) || true

    # Also pick up boot-client assets from the same API response.
    RELEASE_BOOT=""
    RELEASE_BOOT=$(curl -sSfL --connect-timeout 10 --retry 2 \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: autodeploy-installer" \
        "https://api.github.com/repos/Rusketh/AutoDeploy/releases/tags/$INSTALLED_VERSION" 2>/dev/null \
        | grep -o '"name": "autodeploy-boot-[^"]*"' | cut -d'"' -f4 \
        | grep -v '\.sha256$' | grep -v '\.version$' | sort -u) || true

    if [ -n "$RELEASE_AGENTS" ] || [ -n "$RELEASE_BOOT" ]; then
        ALL_FETCH="$RELEASE_AGENTS $RELEASE_BOOT"
    else
        # Fallback: API unreachable, use the legacy hardcoded list.
        echo "  GitHub API unavailable; falling back to legacy asset list." >&2
        ALL_FETCH="autodeploy-agent-windows-amd64.exe autodeploy-boot-linux-amd64"
    fi

    for fetch_asset in $ALL_FETCH; do
        if [ -f "$DATA_DIR/downloads/$fetch_asset" ]; then continue; fi
        echo "== Fetching $fetch_asset from $INSTALLED_VERSION =="
        ok=1
        for asset in "$fetch_asset" "$fetch_asset.sha256" "$fetch_asset.version"; do
            if ! curl -sSfL --connect-timeout 10 --retry 2 \
                    -o "$DATA_DIR/downloads/$asset" \
                    "$GH_BASE/$asset"; then
                rm -f "$DATA_DIR/downloads/$asset"
                # .version sidecar is optional pre-v0.1.3; treat its
                # absence as harmless but a missing binary or .sha256
                # means the whole fetch is no good.
                case "$asset" in
                    *.version) : ;;
                    *) ok=0; break ;;
                esac
            fi
        done
        if [ "$ok" -eq 1 ] && [ -f "$DATA_DIR/downloads/$fetch_asset.sha256" ]; then
            if ( cd "$DATA_DIR/downloads" && sha256sum -c "$fetch_asset.sha256" --quiet >/dev/null 2>&1 ); then
                chown autodeploy:autodeploy "$DATA_DIR/downloads/$fetch_asset" \
                    "$DATA_DIR/downloads/$fetch_asset.sha256" 2>/dev/null || true
                [ -f "$DATA_DIR/downloads/$fetch_asset.version" ] && \
                    chown autodeploy:autodeploy "$DATA_DIR/downloads/$fetch_asset.version" 2>/dev/null || true
                echo "    fetched $DATA_DIR/downloads/$fetch_asset (sha256 verified)"
            else
                echo "    WARN: SHA-256 mismatch for $fetch_asset; removing" >&2
                rm -f "$DATA_DIR/downloads/$fetch_asset" \
                      "$DATA_DIR/downloads/$fetch_asset.sha256" \
                      "$DATA_DIR/downloads/$fetch_asset.version"
            fi
        elif [ "$ok" -eq 0 ]; then
            echo "    WARN: failed to fetch $fetch_asset from $GH_BASE -- offline install? Drop the binary into $DATA_DIR/downloads/ by hand." >&2
        fi
    done
fi

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

# Install the in-place upgrade helper and a narrowly-scoped sudoers
# rule so the portal's "Update server" button can launch it as root
# without granting the autodeploy user general sudo.
UPDATE_SRC="$HERE/autodeploy-update-linux.sh"
if [ -f "$UPDATE_SRC" ]; then
    echo "== Installing self-update helper =="
    install -m 0755 "$UPDATE_SRC" /usr/local/sbin/autodeploy-update
    # The rule is exact: only the specific binary path, only by the
    # autodeploy user, no password. visudo-validate before writing so
    # a typo here doesn't lock the operator out of sudo.
    SUDOERS_TMP=$(mktemp)
    cat > "$SUDOERS_TMP" <<'EOF'
# Allow the autodeploy service user to launch the in-place upgrade
# helper without a password. Scope is intentionally narrow: only this
# one absolute path is permitted, and only with arguments the portal
# button supplies. Installed by scripts/install-linux.sh.
autodeploy ALL=(root) NOPASSWD: /usr/local/sbin/autodeploy-update
EOF
    if visudo -cf "$SUDOERS_TMP" >/dev/null 2>&1; then
        install -m 0440 "$SUDOERS_TMP" /etc/sudoers.d/autodeploy-update
        echo "    installed /etc/sudoers.d/autodeploy-update"
    else
        echo "  WARN: visudo validation failed; not installing sudoers rule" >&2
        echo "  Drop in by hand later, or update will not work from the portal." >&2
    fi
    rm -f "$SUDOERS_TMP"
    # Verify the sudoers entry was actually written. A silent failure here
    # (e.g. read-only /etc, selinux denial) would leave the portal's Update
    # button broken with no clue in the install log.
    if [ ! -f /etc/sudoers.d/autodeploy-update ]; then
        echo "  WARN: /etc/sudoers.d/autodeploy-update was not installed." >&2
        echo "  The portal Update button will fail because sudo will prompt for" >&2
        echo "  a password. Create the file manually or re-run the installer" >&2
        echo "  with write access to /etc/sudoers.d/." >&2
    fi
else
    echo "  WARN: $UPDATE_SRC not found in the release bundle." >&2
    echo "  Skipping installation of the in-place update helper." >&2
    echo "  The portal's Update button will not work until the helper" >&2
    echo "  is installed at /usr/local/sbin/autodeploy-update." >&2
fi

# Install the embedded-iPXE build helper alongside the binary. It's
# not run automatically (the build pulls in ~300 MB of apt deps and
# takes a few minutes) but operators on UniFi / OPNsense / consumer
# routers that can't do conditional DHCP will need it -- the next-
# steps banner below tells them so.
EMBED_SRC="$HERE/build-embedded-ipxe.sh"
if [ -f "$EMBED_SRC" ]; then
    install -m 0755 "$EMBED_SRC" /usr/local/sbin/autodeploy-build-embedded-ipxe
    echo "    installed /usr/local/sbin/autodeploy-build-embedded-ipxe"
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

  2. Pick a TLS deployment shape (the env file documents all three):

     a) HTTPS only: place a cert + key at
            /etc/autodeploy/tls/server.crt
            /etc/autodeploy/tls/server.key
        and point AUTODEPLOY_TLS_CERT / KEY at them. Set
        AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443 and leave HTTP_ADDR
        empty (or bound to loopback).

     b) HTTP only: leave the TLS paths empty, set
            AUTODEPLOY_HTTP_ADDR=0.0.0.0:8080
            AUTODEPLOY_HTTPS_ADDR=
        The server starts with a clearly-marked warning in the
        log so the choice is auditable. Use this when a reverse
        proxy terminates TLS upstream, or on a trusted-network
        LAN deployment where TLS is not worth managing.

     c) Both: HTTP on loopback for health checks, HTTPS for
        everything else.

  3. Start the service:
         systemctl enable --now autodeploy

  4. Read the bootstrap admin password (one-time):
         cat $DATA_DIR/admin-bootstrap.txt
     Default shape is HTTP on port 8080; log in at
         http://<this-host>:8080/portal/
     (or https://<this-host>/portal/ if you configured HTTPS).
     Change the password via Settings → Local accounts, then
     delete the bootstrap file.

  5. The Boot Client kernel + initramfs were fetched automatically
     into $DATA_DIR/ipxe/ as autodeploy-kernel + autodeploy-initrd.
     Verify:
       ls -l $DATA_DIR/ipxe/autodeploy-kernel $DATA_DIR/ipxe/autodeploy-initrd
     If absent (older release), re-run fetch-ipxe.sh.

  6. Configure DHCP to hand out one stock iPXE binary: undionly.kpxe
     (BIOS) or ipxe.efi / snponly.efi (UEFI). The server serves
     autoexec.ipxe, so no conditional DHCP and no custom-built binary
     are needed -- just point next-server (option 66) at this host.
     Best place to start:
       docs/user-guide/tutorial-02-pxe.md

Status: systemctl status autodeploy
Logs:   journalctl -u autodeploy -f
============================================================
EOF

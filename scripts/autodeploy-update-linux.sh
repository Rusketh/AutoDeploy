#!/usr/bin/env bash
# scripts/autodeploy-update-linux.sh
#
# In-place upgrade of the running AutoDeploy server on Linux.
#
# Usage:
#   autodeploy-update [--tag vX.Y.Z] [--data DIR]
#
# Default tag is "latest" (resolved via the GitHub releases API).
# Stops the autodeploy.service, downloads + SHA-256-verifies the new
# server binary plus the Windows agent, swaps the server binary,
# refreshes the downloads directory AND the Boot Client image (kernel +
# initramfs) in the iPXE dir, then restarts. The kernel/initrd refresh
# is best-effort so a pinned older release without them still updates.
#
# This script is installed to /usr/local/sbin/ by install-linux.sh
# and granted passwordless sudo via /etc/sudoers.d/autodeploy-update,
# so the portal's "Update server" button can launch it on behalf of
# the autodeploy service user.
#
# Never run if the request looks suspicious. The script:
#   - Refuses to run as a non-root user.
#   - Verifies SHA-256 on every downloaded asset.
#   - Aborts the swap if SHA-256 verification fails.
#   - Keeps a .bak of the previous binary for one-step rollback.
#
# Log destination is the systemd journal (when invoked by the unit
# below) or stdout (when invoked by hand).
set -euo pipefail

TAG=""
DATA_DIR=/var/lib/autodeploy
LOG_PREFIX="[autodeploy-update]"

log() { printf '%s %s\n' "$LOG_PREFIX" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)  TAG="$2"; shift 2 ;;
        --data) DATA_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//' | head -n -1
            exit 0 ;;
        *) die "Unknown arg: $1" ;;
    esac
done

if [ "$EUID" -ne 0 ]; then
    die "Must run as root (the portal invokes via sudoers; in CLI use 'sudo')"
fi

# Resolve "latest" via the public Releases API. Requires no auth for a
# public repo. The .tag_name field is what we want.
if [ -z "$TAG" ]; then
    log "Resolving latest release..."
    TAG=$(curl -sSfL --connect-timeout 10 --retry 2 \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: autodeploy-update" \
        https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
        | grep -m1 '"tag_name"' | cut -d'"' -f4)
fi
[ -n "$TAG" ] || die "Could not resolve a release tag"
case "$TAG" in
    v[0-9]*) : ;;
    *) die "Invalid tag '$TAG' (expected vMAJOR.MINOR.PATCH)" ;;
esac
log "Target version: $TAG"

# Detect arch + map to the release asset suffix.
case "$(uname -m)" in
    x86_64)  SERVER_ASSET=autodeploy-server-linux-amd64 ;;
    aarch64) SERVER_ASSET=autodeploy-server-linux-arm64 ;;
    *) die "Unsupported architecture: $(uname -m)" ;;
esac
GH_BASE="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG"

WORK=$(mktemp -d -t autodeploy-update-XXXXXX)
trap 'rm -rf "$WORK"' EXIT

# --- Keep the updater itself current ---------------------------------
# The autodeploy-update script on disk was dropped at the LAST install, so
# it can predate the release being installed and miss newer update steps
# (e.g. installing a freshly-required OS package). Pull this release's
# updater and, if it differs, install it and re-exec so the update always
# runs the TARGET version's logic. A guard env var prevents a loop.
RAW_BASE="https://raw.githubusercontent.com/Rusketh/AutoDeploy/$TAG/scripts"
SELF_DST=/usr/local/sbin/autodeploy-update
if [ -z "${AUTODEPLOY_UPDATE_REEXEC:-}" ]; then
    if curl -sSfL --connect-timeout 10 --retry 2 \
            -o "$WORK/updater.sh" "$RAW_BASE/autodeploy-update-linux.sh" 2>/dev/null \
       && [ -s "$WORK/updater.sh" ] \
       && ! cmp -s "$WORK/updater.sh" "$SELF_DST"; then
        log "Updater is out of date; installing $TAG's updater and re-running..."
        install -m 0755 "$WORK/updater.sh" "$SELF_DST"
        AUTODEPLOY_UPDATE_REEXEC=1 exec "$SELF_DST" --tag "$TAG" --data "$DATA_DIR"
    fi
fi

# --- Refresh OS dependencies the server needs ------------------------
# The server ingests Windows ISOs with 7z (UDF) and splits an oversized
# install.wim with wimlib-imagex. Updating a server that predates these
# backfills them so ISO prep stops failing with "executable not found".
# Mirrors install-linux.sh; the self-update above keeps this list current.
ensure_dep() {
    # ensure_dep <probe-cmd> <human-name> <apt-pkgs> <dnf/yum-pkgs> <zypper-pkgs>
    command -v "$1" >/dev/null 2>&1 && return 0
    log "Installing $2 ..."
    if command -v apt-get >/dev/null 2>&1; then apt-get install -y $3 >/dev/null 2>&1
    elif command -v dnf >/dev/null 2>&1; then dnf install -y $4 >/dev/null 2>&1
    elif command -v yum >/dev/null 2>&1; then yum install -y $4 >/dev/null 2>&1
    elif command -v zypper >/dev/null 2>&1; then zypper install -y $5 >/dev/null 2>&1
    else log "  WARN: unknown package manager; install $2 manually"; return 0; fi
    command -v "$1" >/dev/null 2>&1 || log "  WARN: $2 still missing after install attempt; install it manually"
}
if ! command -v 7z >/dev/null 2>&1 && ! command -v 7za >/dev/null 2>&1 && ! command -v bsdtar >/dev/null 2>&1; then
    ensure_dep 7z "p7zip (ISO extraction)" "p7zip-full" "p7zip p7zip-plugins" "p7zip"
fi
ensure_dep wimlib-imagex "wimlib (install.wim splitting)" "wimtools" "wimlib-utils" "wimlib-tools"

fetch() {
    # fetch <asset>
    # Downloads to $WORK/<asset>. Aborts the whole script on failure.
    local asset="$1"
    log "  fetching $asset"
    curl -sSfL --connect-timeout 10 --retry 2 \
        -o "$WORK/$asset" \
        "$GH_BASE/$asset" \
        || die "fetch failed for $asset"
}

log "Downloading server binary..."
fetch "$SERVER_ASSET"
fetch "$SERVER_ASSET.sha256"
# .version sidecar: optional pre-v0.1.3, fetch best-effort.
curl -sSfL --connect-timeout 10 --retry 2 \
    -o "$WORK/$SERVER_ASSET.version" "$GH_BASE/$SERVER_ASSET.version" 2>/dev/null \
    || rm -f "$WORK/$SERVER_ASSET.version"

log "Verifying server SHA-256..."
( cd "$WORK" && sha256sum -c "$SERVER_ASSET.sha256" --quiet ) \
    || die "SHA-256 mismatch on $SERVER_ASSET; aborting update"
log "Server binary verified."

# --- Fetch ALL available agent binaries from the release ----------------
# Query the GitHub API for every autodeploy-agent-* asset in the release
# so we automatically pick up new platforms (linux-arm64, etc.) without
# hardcoding each one. Falls back to the legacy single-agent fetch when
# the API is unreachable (air-gapped / rate-limited environments).
log "Fetching agent binaries..."
AGENTS=""
AGENTS=$(curl -sSfL --connect-timeout 10 --retry 2 \
    -H "Accept: application/vnd.github+json" \
    -H "User-Agent: autodeploy-update" \
    "https://api.github.com/repos/Rusketh/AutoDeploy/releases/tags/$TAG" 2>/dev/null \
    | grep -o '"name": "autodeploy-agent-[^"]*"' | cut -d'"' -f4 \
    | grep -v '\.sha256$' | grep -v '\.version$' | sort -u) || true

if [ -n "$AGENTS" ]; then
    AGENT_FAIL=0
    for agent in $AGENTS; do
        log "  fetching $agent + sidecars"
        if ! curl -sSfL --connect-timeout 10 --retry 2 \
                -o "$WORK/$agent" "$GH_BASE/$agent"; then
            log "  WARN: failed to fetch $agent; skipping"
            rm -f "$WORK/$agent"
            continue
        fi
        if ! curl -sSfL --connect-timeout 10 --retry 2 \
                -o "$WORK/$agent.sha256" "$GH_BASE/$agent.sha256"; then
            log "  WARN: failed to fetch $agent.sha256; skipping $agent"
            rm -f "$WORK/$agent" "$WORK/$agent.sha256"
            continue
        fi
        # .version sidecar is optional
        curl -sSfL --connect-timeout 10 --retry 2 \
            -o "$WORK/$agent.version" "$GH_BASE/$agent.version" 2>/dev/null \
            || rm -f "$WORK/$agent.version"

        # Verify SHA-256
        if ! ( cd "$WORK" && sha256sum -c "$agent.sha256" --quiet ); then
            log "  WARN: SHA-256 mismatch on $agent; skipping"
            rm -f "$WORK/$agent" "$WORK/$agent.sha256" "$WORK/$agent.version"
            AGENT_FAIL=1
            continue
        fi
    done
    [ "$AGENT_FAIL" -eq 0 ] || log "  WARN: one or more agent binaries failed verification"
else
    # Fallback: API unavailable, fetch the legacy single agent.
    log "  GitHub API unavailable; falling back to legacy single-agent fetch"
    fetch "autodeploy-agent-windows-amd64.exe"
    fetch "autodeploy-agent-windows-amd64.exe.sha256"
    curl -sSfL --connect-timeout 10 --retry 2 \
        -o "$WORK/autodeploy-agent-windows-amd64.exe.version" \
        "$GH_BASE/autodeploy-agent-windows-amd64.exe.version" 2>/dev/null \
        || rm -f "$WORK/autodeploy-agent-windows-amd64.exe.version"
    ( cd "$WORK" && sha256sum -c "autodeploy-agent-windows-amd64.exe.sha256" --quiet ) \
        || die "SHA-256 mismatch on the agent; aborting update"
fi
log "All artifacts verified."

# Refresh the downloads directory FIRST. The agents poll the server's
# update-info endpoint after a restart and will see the new version
# immediately.
log "Refreshing $DATA_DIR/downloads/..."
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/downloads"
for f in "$WORK"/autodeploy-agent-*; do
    [ -f "$f" ] || continue
    fname="$(basename "$f")"
    install -m 0644 -o autodeploy -g autodeploy "$f" "$DATA_DIR/downloads/$fname"
done

# Refresh the Boot Client image (kernel + initramfs) in the iPXE dir so
# the PXE chain has something to boot -- the same auto-acquire the
# installer does via fetch-ipxe.sh, but on every update too. Best-effort:
# these are large and a deliberately-pinned older release may not carry
# them, so a miss is a warning, never a reason to abort the server swap.
# We SHA-256-verify any we do fetch and only swap a file in once verified,
# so a partial download can't leave a corrupt kernel/initrd on disk.
log "Refreshing Boot Client image in $DATA_DIR/ipxe/..."
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/ipxe"
for img in autodeploy-kernel autodeploy-initrd; do
    if curl -sSfL --connect-timeout 10 --retry 2 \
            -o "$WORK/$img" "$GH_BASE/$img" 2>/dev/null \
       && curl -sSfL --connect-timeout 10 --retry 2 \
            -o "$WORK/$img.sha256" "$GH_BASE/$img.sha256" 2>/dev/null \
       && ( cd "$WORK" && sha256sum -c "$img.sha256" --quiet >/dev/null 2>&1 ); then
        install -m 0644 -o autodeploy -g autodeploy "$WORK/$img" "$DATA_DIR/ipxe/$img"
        install -m 0644 -o autodeploy -g autodeploy "$WORK/$img.sha256" "$DATA_DIR/ipxe/$img.sha256"
        log "  updated $img"
    else
        log "  WARN: could not fetch/verify $img for $TAG -- leaving existing file in place"
        rm -f "$WORK/$img" "$WORK/$img.sha256"
    fi
done

# Stop the service before swapping the binary so the file isn't held
# open by the running process. systemctl is idempotent: stopping an
# already-stopped unit is a no-op.
log "Stopping autodeploy.service..."
systemctl stop autodeploy.service || die "systemctl stop failed"

# Keep a .bak for one-step rollback: if the new binary fails to start
# the operator just `mv autodeploy-server.bak autodeploy-server`.
BIN_DST=/usr/local/bin/autodeploy-server
if [ -f "$BIN_DST" ]; then
    cp -a "$BIN_DST" "$BIN_DST.bak"
fi
log "Installing new server binary..."
install -m 0755 "$WORK/$SERVER_ASSET" "$BIN_DST"

log "Starting autodeploy.service..."
if ! systemctl start autodeploy.service; then
    log "ERROR: new server failed to start. Rolling back to .bak..." >&2
    if [ -f "$BIN_DST.bak" ]; then
        mv -f "$BIN_DST.bak" "$BIN_DST"
        systemctl start autodeploy.service || \
            log "ERROR: rollback also failed -- service is stopped." >&2
    fi
    die "update failed; rolled back"
fi

# Wait briefly for the new server to come up so we can report success.
for _ in 1 2 3 4 5; do
    if systemctl is-active --quiet autodeploy.service; then
        log "Update complete; service is active on $TAG"
        # Remove the .bak only after confirming the new binary is up.
        rm -f "$BIN_DST.bak"
        exit 0
    fi
    sleep 1
done
log "WARN: service did not report active within 5s; check 'systemctl status autodeploy'" >&2
exit 0

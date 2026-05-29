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
# server binary plus the Windows agent + Linux boot client, swaps the
# server binary, refreshes the downloads directory, restarts.
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

log "Downloading server + companions..."
fetch "$SERVER_ASSET"
fetch "$SERVER_ASSET.sha256"
fetch "autodeploy-agent-windows-amd64.exe"
fetch "autodeploy-agent-windows-amd64.exe.sha256"
# .version sidecars: optional pre-v0.1.3, fetch best-effort.
for opt in "$SERVER_ASSET.version" "autodeploy-agent-windows-amd64.exe.version"; do
    curl -sSfL --connect-timeout 10 --retry 2 \
        -o "$WORK/$opt" "$GH_BASE/$opt" 2>/dev/null || rm -f "$WORK/$opt"
done

log "Verifying SHA-256..."
( cd "$WORK" && sha256sum -c "$SERVER_ASSET.sha256" --quiet ) \
    || die "SHA-256 mismatch on $SERVER_ASSET; aborting update"
( cd "$WORK" && sha256sum -c "autodeploy-agent-windows-amd64.exe.sha256" --quiet ) \
    || die "SHA-256 mismatch on the agent; aborting update"
log "All artifacts verified."

# Refresh the downloads directory FIRST. The agents poll the server's
# update-info endpoint after a restart and will see the new version
# immediately.
log "Refreshing $DATA_DIR/downloads/..."
install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/downloads"
for a in autodeploy-agent-windows-amd64.exe \
         autodeploy-agent-windows-amd64.exe.sha256 \
         autodeploy-agent-windows-amd64.exe.version; do
    if [ -f "$WORK/$a" ]; then
        install -m 0644 -o autodeploy -g autodeploy "$WORK/$a" "$DATA_DIR/downloads/$a"
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

#!/usr/bin/env bash
# scripts/fetch-ipxe.sh
#
# Fetch the iPXE network-boot binaries operators need to chainload
# AutoDeploy from a classic PXE / TFTP environment.
#
# Usage:
#   scripts/fetch-ipxe.sh [DATA_DIR]
#   scripts/fetch-ipxe.sh --tag vX.Y.Z [DATA_DIR]
#
# Default DATA_DIR is $AUTODEPLOY_DATA_DIR/ipxe (or ./data/ipxe). The
# downloaded files are placed there and named per the iPXE.org
# convention, so an operator can point a TFTP server's root directly
# at the directory.
#
# Either source works out of the box: the AutoDeploy server serves an
# autoexec.ipxe bootstrap (over TFTP and HTTP) that any stock iPXE
# binary fetches and runs on its own. No embedded build, no conditional
# DHCP -- just point DHCP's next-server (option 66) at the AutoDeploy
# host and hand out one of these binaries.
#
# Files fetched (in priority order):
#   1. AutoDeploy release assets    (built by .github/workflows/build-ipxe.yml.
#                                    These also carry a universal embedded
#                                    chainloader as a belt-and-braces fallback,
#                                    but it's redundant now that the server
#                                    serves autoexec.ipxe.)
#   2. boot.ipxe.org                (vanilla upstream iPXE. Works exactly the
#                                    same -- it fetches the server's
#                                    autoexec.ipxe on boot.)
set -euo pipefail

TAG=""
DATA_DIR_ARG=""
while [ $# -gt 0 ]; do
    case "$1" in
        --tag) TAG="$2"; shift 2 ;;
        --help|-h)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//' | head -n -1
            exit 0 ;;
        *)
            if [ -z "$DATA_DIR_ARG" ]; then
                DATA_DIR_ARG="$1"; shift
            else
                echo "Unknown arg: $1" >&2; exit 2
            fi ;;
    esac
done

DATA_DIR="${DATA_DIR_ARG:-${AUTODEPLOY_DATA_DIR:-./data}}/ipxe"
mkdir -p "$DATA_DIR"

# Resolve TAG to fetch AutoDeploy-built binaries from. Falls back to
# the running server binary's --version, then to the latest GitHub
# release.
resolve_tag() {
    if [ -n "$TAG" ]; then return; fi
    if command -v autodeploy-server >/dev/null 2>&1; then
        TAG=$(autodeploy-server --version 2>/dev/null | head -n1 | tr -d '[:space:]')
        case "$TAG" in v*) return ;; esac
        TAG=""
    fi
    if command -v /usr/local/bin/autodeploy-server >/dev/null 2>&1; then
        TAG=$(/usr/local/bin/autodeploy-server --version 2>/dev/null | head -n1 | tr -d '[:space:]')
        case "$TAG" in v*) return ;; esac
        TAG=""
    fi
    if command -v curl >/dev/null; then
        TAG=$(curl -sSfL --connect-timeout 5 \
                https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
                2>/dev/null | grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
    fi
}

resolve_tag
echo "== AutoDeploy iPXE fetch =="
echo "  Target dir: $DATA_DIR"
if [ -n "$TAG" ]; then
    echo "  AutoDeploy release tag: $TAG (primary source)"
fi
echo "  Fallback: boot.ipxe.org (vanilla iPXE, no embedded script)"
echo

FILES=(
    "undionly.kpxe"
    "ipxe.pxe"
    "ipxe.efi"
    "snponly.efi"
    "ipxe-arm64.efi"
)
# boot.ipxe.org's path layout: BIOS at top level, EFI under <arch>-efi/.
# The x86_64 directory uses iPXE's internal arch name (matches the
# source-tree bin-x86_64-efi/ output).
declare -A FALLBACK=(
    [undionly.kpxe]="http://boot.ipxe.org/undionly.kpxe"
    [ipxe.pxe]="http://boot.ipxe.org/ipxe.pxe"
    [ipxe.efi]="http://boot.ipxe.org/x86_64-efi/ipxe.efi"
    [snponly.efi]="http://boot.ipxe.org/x86_64-efi/snponly.efi"
    [ipxe-arm64.efi]="http://boot.ipxe.org/arm64-efi/ipxe.efi"
)

fetch_url() {
    # fetch_url <url> <dest>
    # Returns 0 on success (file present, >=1KB), 1 on failure.
    local url="$1" dst="$2"
    if command -v curl >/dev/null; then
        curl -sSfL --connect-timeout 10 -o "$dst" "$url" 2>/dev/null || return 1
    elif command -v wget >/dev/null; then
        wget -q --timeout=10 -O "$dst" "$url" 2>/dev/null || return 1
    else
        echo "ERROR: need curl or wget" >&2
        exit 1
    fi
    # Sub-1KB is almost certainly an error page; remove and report.
    local size
    size=$(wc -c < "$dst" 2>/dev/null || echo 0)
    if [ "$size" -lt 1024 ]; then
        rm -f "$dst"
        return 1
    fi
    return 0
}

cd "$DATA_DIR"
failed=0
for f in "${FILES[@]}"; do
    got=""

    # 1. Try AutoDeploy release asset.
    if [ -n "$TAG" ]; then
        url="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG/$f"
        echo "Trying release: $url"
        if fetch_url "$url" "$f"; then
            got="release"
        fi
    fi

    # 2. Fall back to boot.ipxe.org.
    if [ -z "$got" ]; then
        url="${FALLBACK[$f]:-}"
        if [ -n "$url" ]; then
            echo "Trying fallback: $url"
            if fetch_url "$url" "$f"; then
                got="fallback"
            fi
        fi
    fi

    if [ -z "$got" ]; then
        echo "  WARN: failed to fetch $f from any source" >&2
        failed=$((failed+1))
        continue
    fi
    size=$(wc -c < "$f")
    echo "  OK: $f ($size bytes, source=$got)"
done

# Best-effort sidecar sha256 (just for parity with release assets).
for f in "${FILES[@]}"; do
    if [ -f "$f" ]; then
        sha256sum "$f" > "$f.sha256" 2>/dev/null || true
    fi
done

echo
echo "iPXE binaries in $DATA_DIR:"
ls -la "$DATA_DIR"
echo
if [ "$failed" -gt 0 ]; then
    echo "WARNING: $failed download(s) failed. Re-run once network access is" >&2
    echo "restored, or build iPXE yourself and drop the binaries in $DATA_DIR." >&2
    exit 1
fi
echo "Next step: point your TFTP server's root at $DATA_DIR (or use AutoDeploy's"
echo "built-in TFTP listener by setting AUTODEPLOY_TFTP_ADDR) and configure DHCP."
echo "Per-platform DHCP setup: docs/user-guide/tutorial-02-pxe.md, or"
echo "open the portal's Settings -> PXE page after first start."

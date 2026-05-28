#!/usr/bin/env bash
# scripts/fetch-ipxe.sh
#
# Fetch the iPXE network-boot binaries operators need to chainload
# AutoDeploy from a classic PXE / TFTP environment.
#
# Usage:
#   scripts/fetch-ipxe.sh [DATA_DIR]
#
# Default DATA_DIR is $AUTODEPLOY_DATA_DIR/ipxe (or ./data/ipxe). The
# downloaded files are placed there and named per the iPXE.org
# convention, so an operator can point a TFTP server's root directly
# at the directory.
#
# Files fetched:
#   undionly.kpxe   - legacy BIOS PXE chainload (most common)
#   ipxe.pxe        - alternative BIOS chainload (use when undionly.kpxe
#                     hangs on the NIC; PXE NBP variant)
#   ipxe.efi        - UEFI x64 chainload
#   snponly.efi     - UEFI x64 chainload using only the SNP driver (use
#                     when ipxe.efi fails to bring the NIC up)
#   ipxe-arm64.efi  - UEFI arm64 chainload
#
# All files come from boot.ipxe.org's pre-built releases. If your
# environment cannot reach the internet, build iPXE yourself from
# https://github.com/ipxe/ipxe and drop the binaries in this directory.
set -euo pipefail

DATA_DIR="${1:-${AUTODEPLOY_DATA_DIR:-./data}}/ipxe"
mkdir -p "$DATA_DIR"

BASE="http://boot.ipxe.org"
FILES=(
    "undionly.kpxe"
    "ipxe.pxe"
    "ipxe.efi"
    "snponly.efi"
    "arm64-efi/ipxe.efi:ipxe-arm64.efi"
)

cd "$DATA_DIR"
failed=0
for spec in "${FILES[@]}"; do
    src="${spec%%:*}"
    dst="${spec##*:}"
    if [ "$src" = "$dst" ]; then
        dst="$(basename "$src")"
    fi
    echo "Fetching $BASE/$src -> $DATA_DIR/$dst"
    # Per-file failure should not abort the whole batch -- a single
    # broken URL or transient 5xx is recoverable on a later re-run.
    fetch_ok=1
    if command -v curl >/dev/null; then
        curl -sSfL -o "$dst" "$BASE/$src" || fetch_ok=0
    elif command -v wget >/dev/null; then
        wget -q -O "$dst" "$BASE/$src" || fetch_ok=0
    else
        echo "ERROR: need curl or wget" >&2
        exit 1
    fi
    if [ "$fetch_ok" -eq 0 ]; then
        echo "  WARN: failed to fetch $src" >&2
        failed=$((failed+1))
        rm -f "$dst"
        continue
    fi
    # A sub-1KB file is almost certainly an error page, not a real
    # binary. Remove it so the next re-run can replace it.
    size=$(wc -c < "$dst" 2>/dev/null || echo 0)
    if [ "$size" -lt 1024 ]; then
        echo "  WARN: $dst is only $size bytes -- looks like an error response, not a real binary." >&2
        rm -f "$dst"
        failed=$((failed+1))
    fi
done

echo
echo "iPXE binaries in $DATA_DIR:"
ls -la "$DATA_DIR"
echo
if [ "$failed" -gt 0 ]; then
    echo "WARNING: $failed download(s) failed. Re-run this script once network access is restored, or" >&2
    echo "build iPXE yourself from https://github.com/ipxe/ipxe and drop the binaries in $DATA_DIR." >&2
    exit 1
fi
echo "Next step: point your TFTP server's root at $DATA_DIR (or use AutoDeploy's"
echo "built-in TFTP listener by setting AUTODEPLOY_TFTP_ADDR) and configure DHCP."
echo "See docs/user-guide/pxe-setup.md for the DHCP config patterns."

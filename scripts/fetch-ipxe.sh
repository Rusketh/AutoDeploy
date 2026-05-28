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
for spec in "${FILES[@]}"; do
    src="${spec%%:*}"
    dst="${spec##*:}"
    if [ "$src" = "$dst" ]; then
        # No rename — use the basename.
        dst="$(basename "$src")"
    fi
    echo "Fetching $BASE/$src -> $DATA_DIR/$dst"
    if command -v curl >/dev/null; then
        curl -sSfL -o "$dst" "$BASE/$src"
    elif command -v wget >/dev/null; then
        wget -q -O "$dst" "$BASE/$src"
    else
        echo "ERROR: need curl or wget" >&2
        exit 1
    fi
done

echo
echo "iPXE binaries ready in $DATA_DIR:"
ls -la "$DATA_DIR"
echo
echo "Next step: point your TFTP server's root at $DATA_DIR (or copy"
echo "the files to your existing TFTP root) and configure DHCP. See"
echo "docs/user-guide/pxe-setup.md for the DHCP config patterns."

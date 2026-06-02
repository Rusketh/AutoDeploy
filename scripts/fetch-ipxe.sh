#!/usr/bin/env bash
# scripts/fetch-ipxe.sh
#
# Fetch the OFFICIAL, signed iPXE network-boot binaries operators need
# to chainload AutoDeploy from a PXE / TFTP / UEFI HTTP environment.
#
# Usage:
#   scripts/fetch-ipxe.sh [DATA_DIR]
#   scripts/fetch-ipxe.sh --ipxe-tag vX.Y.Z [DATA_DIR]
#   scripts/fetch-ipxe.sh --ipxe-tar /path/to/ipxeboot.tar.gz [DATA_DIR]
#   scripts/fetch-ipxe.sh --tag vA.B.C [DATA_DIR]   # AutoDeploy boot-image tag
#
# Default DATA_DIR is $AUTODEPLOY_DATA_DIR/ipxe (or ./data/ipxe). The
# files are placed there with the names AutoDeploy serves over TFTP /
# HTTP, so an operator can point a TFTP server's root directly at the
# directory.
#
# Why the official archive?
#   AutoDeploy used to ship its own iPXE builds with a boot script
#   embedded. Embedding modifies the binary, so those builds could
#   never be signed -- which meant UEFI Secure Boot had to be disabled
#   to network-boot. The iPXE project now publishes a signed release
#   (ipxeboot.tar.gz) containing:
#     - shimx64.efi / shimaa64.efi  -- a Microsoft-signed shim
#     - ipxe.efi / snponly.efi      -- iPXE signed with the iPXE CA that
#                                      is embedded in (and trusted by)
#                                      the shim
#   Booting shim -> ipxe.efi keeps the Secure Boot chain of trust intact.
#   And because AutoDeploy already serves autoexec.ipxe dynamically (over
#   both TFTP and HTTP), the STOCK signed binaries chainload to AutoDeploy
#   with no embedding and no per-install bake-in.
#
#   NOTE: a signed iPXE will, under Secure Boot, only go on to boot a
#   signed next stage. The AutoDeploy boot image (autodeploy-kernel) is
#   not yet signed, so an end-to-end Secure Boot deployment still needs
#   the kernel handled separately (signed kernel / shim+MOK). Until then,
#   the signed shim gets you a verified bootloader; Secure Boot must be
#   off (or the kernel enrolled) for the kernel stage to load. See
#   docs/install/pxe-and-boot.md.
#
# Source:
#   iPXE GitHub release asset ipxeboot.tar.gz (default: latest).
set -euo pipefail

IPXE_TAG=""          # upstream iPXE release tag; empty => latest
IPXE_TAR=""          # pre-downloaded ipxeboot.tar.gz (air-gapped)
TAG=""               # AutoDeploy release tag, for the boot image
DATA_DIR_ARG=""
while [ $# -gt 0 ]; do
    case "$1" in
        --ipxe-tag) IPXE_TAG="$2"; shift 2 ;;
        --ipxe-tar) IPXE_TAR="$2"; shift 2 ;;
        --tag)      TAG="$2"; shift 2 ;;
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

fetch_url() {
    # fetch_url <url> <dest>; returns 0 on success (file >= 1KB).
    local url="$1" dst="$2"
    if command -v curl >/dev/null; then
        curl -sSfL --connect-timeout 15 -o "$dst" "$url" 2>/dev/null || return 1
    elif command -v wget >/dev/null; then
        wget -q --timeout=15 -O "$dst" "$url" 2>/dev/null || return 1
    else
        echo "ERROR: need curl or wget" >&2; exit 1
    fi
    local size; size=$(wc -c < "$dst" 2>/dev/null || echo 0)
    if [ "$size" -lt 1024 ]; then rm -f "$dst"; return 1; fi
    return 0
}

echo "== AutoDeploy iPXE fetch (official signed binaries) =="
echo "  Target dir: $DATA_DIR"

# ---------------------------------------------------------------------------
# 1. Obtain ipxeboot.tar.gz (official iPXE signed release).
# ---------------------------------------------------------------------------
WORK=$(mktemp -d -t ipxeboot-XXXXXX)
trap 'rm -rf "$WORK"' EXIT
ARCHIVE="$WORK/ipxeboot.tar.gz"

if [ -n "$IPXE_TAR" ]; then
    echo "  Using local archive: $IPXE_TAR"
    cp "$IPXE_TAR" "$ARCHIVE"
else
    if [ -n "$IPXE_TAG" ]; then
        IPXE_URL="https://github.com/ipxe/ipxe/releases/download/$IPXE_TAG/ipxeboot.tar.gz"
        echo "  iPXE release tag: $IPXE_TAG"
    else
        IPXE_URL="https://github.com/ipxe/ipxe/releases/latest/download/ipxeboot.tar.gz"
        echo "  iPXE release tag: latest"
    fi
    echo "  Downloading $IPXE_URL"
    if ! fetch_url "$IPXE_URL" "$ARCHIVE"; then
        echo "ERROR: could not download ipxeboot.tar.gz from iPXE." >&2
        echo "Re-run once you have network access, pass --ipxe-tar with a" >&2
        echo "pre-downloaded archive, or drop the binaries into $DATA_DIR by hand." >&2
        exit 1
    fi
fi

EXTRACT="$WORK/extract"
mkdir -p "$EXTRACT"
tar xzf "$ARCHIVE" -C "$EXTRACT"

# The archive lays binaries out per-arch (x86_64/, x86_64-sb/, arm64/,
# arm64-sb/, ...) under a top-level ipxeboot/ directory. Locate the
# directory that holds the arch trees rather than assuming the nesting.
SB64=$(find "$EXTRACT" -type d -name 'x86_64-sb' | head -1)
if [ -z "$SB64" ]; then
    echo "ERROR: ipxeboot.tar.gz layout unexpected (no x86_64-sb/)." >&2
    exit 1
fi
BASE=$(dirname "$SB64")
echo "  Archive arch root: $BASE"

# ---------------------------------------------------------------------------
# 2. Place the files with AutoDeploy's serving names.
#
# Naming / Secure Boot notes:
#   - ipxe.efi / snponly.efi are the iPXE-CA-signed binaries. With Secure
#     Boot OFF the firmware loads them directly; with Secure Boot ON they
#     are loaded (and verified) by the shim.
#   - shimx64.efi is the Microsoft-signed shim. The shim picks which iPXE
#     to load from the FILENAME it was started as: handed out as
#     ipxe-shim.efi it loads ipxe.efi; as snponly-shim.efi it loads
#     snponly.efi. shimx64.efi itself defaults to ipxe.efi. The three are
#     byte-identical copies of the one signed shim.
#   - undionly.kpxe / ipxe.pxe are legacy BIOS NBPs (BIOS has no Secure
#     Boot, so these are unsigned by nature).
#   - arm64 Secure Boot needs its own ipxe.efi alongside its shim, which
#     would collide with the x86_64 ipxe.efi in a flat directory, so the
#     arm64 Secure Boot trio lives under arm64-sb/.
# ---------------------------------------------------------------------------
cd "$DATA_DIR"

place() {
    # place <src> <dst>
    local src="$1" dst="$2"
    if [ ! -f "$src" ]; then
        echo "  WARN: missing in archive: ${src#$BASE/}" >&2
        return 1
    fi
    install -m 0644 "$src" "$dst"
    sha256sum "$dst" > "$dst.sha256" 2>/dev/null || true
    echo "  OK: $dst ($(wc -c < "$dst") bytes)"
}

failed=0
# Legacy BIOS (no Secure Boot).
place "$BASE/x86_64/undionly.kpxe" "undionly.kpxe" || failed=$((failed+1))
place "$BASE/x86_64/ipxe.pxe"      "ipxe.pxe"      || failed=$((failed+1))
# UEFI x64 iPXE (signed; boots directly with SB off, via shim with SB on).
place "$BASE/x86_64-sb/ipxe.efi"    "ipxe.efi"    || failed=$((failed+1))
place "$BASE/x86_64-sb/snponly.efi" "snponly.efi" || failed=$((failed+1))
# UEFI x64 Secure Boot shim (Microsoft-signed). One binary, three names.
place "$BASE/x86_64-sb/shimx64.efi" "shimx64.efi"      || failed=$((failed+1))
place "$BASE/x86_64-sb/shimx64.efi" "ipxe-shim.efi"    || failed=$((failed+1))
place "$BASE/x86_64-sb/shimx64.efi" "snponly-shim.efi" || failed=$((failed+1))
# UEFI arm64 iPXE (signed; standalone for SB-off / key-enrolled arm64).
place "$BASE/arm64/ipxe.efi" "ipxe-arm64.efi" || failed=$((failed+1))

# UEFI arm64 Secure Boot trio (subdirectory; shim loads ipxe.efi/snponly.efi
# from the same directory it was started in). Best-effort -- arm64 Secure
# Boot is uncommon for this workload, so a missing arm64-sb/ is a soft warn.
if [ -d "$BASE/arm64-sb" ]; then
    mkdir -p arm64-sb
    install -m 0644 "$BASE/arm64-sb/ipxe.efi"     arm64-sb/ipxe.efi        2>/dev/null || true
    install -m 0644 "$BASE/arm64-sb/snponly.efi"  arm64-sb/snponly.efi     2>/dev/null || true
    install -m 0644 "$BASE/arm64-sb/shimaa64.efi" arm64-sb/shimaa64.efi    2>/dev/null || true
    install -m 0644 "$BASE/arm64-sb/shimaa64.efi" arm64-sb/ipxe-shim.efi   2>/dev/null || true
    install -m 0644 "$BASE/arm64-sb/shimaa64.efi" arm64-sb/snponly-shim.efi 2>/dev/null || true
    echo "  OK: arm64-sb/ (arm64 Secure Boot shim + iPXE)"
fi

# ---------------------------------------------------------------------------
# 3. Boot Client image: the prebuilt kernel + initramfs that boots straight
#    into autodeploy-boot. These are AutoDeploy release-only assets (built by
#    .github/workflows/build-bootimage.yml). Without them iPXE chainloads but
#    has nothing to boot. Missing ones are a soft warning -- an operator can
#    build their own with scripts/initramfs/build-initramfs.sh.
# ---------------------------------------------------------------------------
resolve_tag() {
    if [ -n "$TAG" ]; then return; fi
    for cand in autodeploy-server /usr/local/bin/autodeploy-server; do
        if command -v "$cand" >/dev/null 2>&1; then
            TAG=$("$cand" --version 2>/dev/null | head -n1 | tr -d '[:space:]')
            case "$TAG" in v*) return ;; esac
            TAG=""
        fi
    done
    if command -v curl >/dev/null; then
        TAG=$(curl -sSfL --connect-timeout 5 \
                https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
                2>/dev/null | grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
    fi
}
resolve_tag

# autodeploy-shim.efi is the Microsoft-signed Ubuntu shim used by the
# boot.ipxe `shim` command to verify the kernel under UEFI Secure Boot.
# Like the kernel/initrd it is a release-only asset; a missing one only
# affects Secure Boot machines (a soft warning below).
BOOTIMG=("autodeploy-kernel" "autodeploy-initrd" "autodeploy-shim.efi")
bootimg_missing=0
for f in "${BOOTIMG[@]}"; do
    if [ -n "$TAG" ]; then
        url="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG/$f"
        echo "Trying boot image: $url"
        if fetch_url "$url" "$f"; then
            echo "  OK: $f ($(wc -c < "$f") bytes, source=release $TAG)"
            sha256sum "$f" > "$f.sha256" 2>/dev/null || true
            continue
        fi
    fi
    echo "  WARN: could not fetch $f (AutoDeploy release-only asset)" >&2
    bootimg_missing=$((bootimg_missing+1))
done
if [ "$bootimg_missing" -gt 0 ]; then
    echo "NOTE: Boot image asset(s) missing. iPXE will chainload but have" >&2
    echo "nothing to boot until autodeploy-kernel + autodeploy-initrd are" >&2
    echo "present in $DATA_DIR; autodeploy-shim.efi is additionally needed for" >&2
    echo "UEFI Secure Boot machines. Re-run with --tag vX.Y.Z once a release" >&2
    echo "with those assets exists, or build them with build-initramfs.sh." >&2
fi

echo
echo "Boot files in $DATA_DIR:"
ls -la "$DATA_DIR"
echo
if [ "$failed" -gt 0 ]; then
    echo "WARNING: $failed iPXE file(s) were missing from the archive." >&2
    exit 1
fi
echo "Next steps:"
echo "  - Point your TFTP server's root at $DATA_DIR (or use AutoDeploy's"
echo "    built-in TFTP listener via AUTODEPLOY_TFTP_ADDR) and configure DHCP."
echo "  - Secure Boot: hand out ipxe-shim.efi (UEFI x64) instead of ipxe.efi."
echo "  - DHCP setup: open the portal's Settings -> PXE page, or see"
echo "    docs/install/pxe-and-boot.md."

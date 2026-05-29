#!/usr/bin/env bash
# scripts/build-embedded-ipxe.sh
#
# Build iPXE binaries with a boot script EMBEDDED so they chainload
# straight to AutoDeploy without requiring DHCP to differentiate
# between firmware-PXE clients and iPXE clients. This is what
# operators on UniFi / OPNsense / consumer routers / Microsoft DHCP
# without custom Vendor Classes need -- those DHCP servers can only
# hand back ONE bootfile per network, which makes the classic
# "firmware fetches iPXE, iPXE chainloads to HTTP" pattern impossible
# without help.
#
# Usage:
#   sudo scripts/build-embedded-ipxe.sh [AUTODEPLOY_URL]
#
# AUTODEPLOY_URL is the base URL of the server, e.g.
# http://192.168.20.60:8080. If omitted, the script reads
# AUTODEPLOY_HTTP_ADDR from /etc/default/autodeploy and uses the
# host's primary IP. Pass the URL explicitly if you have a DNS name
# or HTTPS endpoint you'd rather embed.
#
# Output binaries land in $DATA_DIR/ipxe/ (default
# /var/lib/autodeploy/ipxe/). The five files we cover are the same
# ones fetch-ipxe.sh manages:
#
#   undionly.kpxe    (legacy BIOS)
#   ipxe.pxe         (legacy BIOS alternative)
#   ipxe.efi         (UEFI x64)
#   snponly.efi      (UEFI x64, SNP-only driver -- works on more NICs)
#   ipxe-arm64.efi   (UEFI arm64)
#
# Each is built with EMBED= pointing at a tiny boot script that does:
#
#   dhcp
#   chain <AUTODEPLOY_URL>/ipxe/boot.ipxe
#
# Build takes 2-3 minutes on a small VM. The toolchain footprint is
# ~300 MB of apt packages -- one-time cost.
set -euo pipefail

DATA_DIR=/var/lib/autodeploy
URL=""

while [ $# -gt 0 ]; do
    case "$1" in
        --data) DATA_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//' | head -n -1
            exit 0 ;;
        *) URL="$1"; shift ;;
    esac
done

if [ "$EUID" -ne 0 ]; then
    echo "Must run as root (drops binaries under $DATA_DIR/ipxe/)." >&2
    exit 1
fi

# Auto-detect URL from /etc/default/autodeploy when not passed.
if [ -z "$URL" ]; then
    if [ -r /etc/default/autodeploy ]; then
        addr=$(grep -E '^AUTODEPLOY_HTTP_ADDR=' /etc/default/autodeploy | tail -1 | cut -d= -f2- | tr -d '"' | tr -d "'" | tr -d ' ')
    fi
    if [ -z "${addr:-}" ]; then
        addr=$(grep -E '^AUTODEPLOY_HTTPS_ADDR=' /etc/default/autodeploy 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"' | tr -d "'" | tr -d ' ')
        scheme="https"
    else
        scheme="http"
    fi
    if [ -z "${addr:-}" ]; then
        echo "Could not auto-detect AutoDeploy URL. Pass it explicitly:" >&2
        echo "  $0 http://your-server:8080" >&2
        exit 2
    fi
    # Replace 0.0.0.0 or :PORT with the host's primary outbound IP.
    port="${addr##*:}"
    host="${addr%:*}"
    if [ -z "$host" ] || [ "$host" = "0.0.0.0" ] || [ "$host" = "::" ]; then
        host=$(ip -o -4 route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}')
        if [ -z "$host" ]; then
            echo "Could not determine host IP. Pass the URL explicitly:" >&2
            echo "  $0 http://192.168.x.x:$port" >&2
            exit 2
        fi
    fi
    URL="${scheme}://${host}:${port}"
fi

case "$URL" in
    http://*|https://*) : ;;
    *) echo "URL must start with http:// or https://: $URL" >&2; exit 2 ;;
esac

echo "== Embedded iPXE build =="
echo "  AutoDeploy URL: $URL"
echo "  Target dir:     $DATA_DIR/ipxe/"
echo

# Check toolchain. iPXE's build needs:
#   - GCC, binutils, GNU Make (build-essential)
#   - perl (for src/util/* helpers)
#   - liblzma-dev (header for compression at link time)
#   - gcc-multilib (BIOS targets compile with -m32; without the 32-bit
#     libs on a 64-bit host, undionly.kpxe / ipxe.pxe builds fail with
#     "fatal error: bits/libc-header-start.h: No such file or directory"
#     or "/usr/bin/ld: cannot find -lgcc")
need_apt=()
command -v gcc      >/dev/null || need_apt+=(build-essential)
command -v git      >/dev/null || need_apt+=(git)
command -v perl     >/dev/null || need_apt+=(perl)
ldconfig -p 2>/dev/null | grep -q liblzma.so || need_apt+=(liblzma-dev)
# Test for 32-bit support directly rather than guessing the package name
# -- some distros call it gcc-multilib, others lib32gcc-s1 + libc6-dev-i386.
if ! echo 'int main(void){return 0;}' | gcc -m32 -x c -o /dev/null - 2>/dev/null; then
    need_apt+=(gcc-multilib)
fi
if [ ${#need_apt[@]} -gt 0 ]; then
    if command -v apt-get >/dev/null; then
        echo "== Installing build toolchain (${need_apt[*]}) =="
        apt-get update
        apt-get install -y "${need_apt[@]}"
    else
        echo "Need: ${need_apt[*]}" >&2
        echo "Install equivalents on your distro and re-run." >&2
        exit 3
    fi
fi

# Clone iPXE shallow. The repo is ~70 MB so this is quick.
WORK=$(mktemp -d -t ipxe-build-XXXXXX)
trap 'rm -rf "$WORK"' EXIT
echo "== Cloning iPXE into $WORK =="
git clone --depth 1 https://github.com/ipxe/ipxe.git "$WORK/ipxe"

# Write the embedded boot script. It does:
#   1. DHCP (gets an IP if iPXE didn't already have one).
#   2. Chain to AutoDeploy. The server's /ipxe/boot.ipxe returns
#      another iPXE script that points at the AutoDeploy Boot
#      Client kernel + initramfs.
#
# We could also `set base ${URL}` and use that in case AutoDeploy's
# boot.ipxe wants to override its base URL via ${base}, but the
# server handles base-URL detection from the incoming Host header
# so a plain chain is enough.
cat > "$WORK/autoexec.ipxe" <<EOF
#!ipxe
echo Loaded embedded AutoDeploy bootstrap
ifstat ||
dhcp ||
echo Chainloading $URL/ipxe/boot.ipxe ...
chain --replace --autofree $URL/ipxe/boot.ipxe ||
echo === iPXE shell (chainload failed) ===
shell
EOF

echo
echo "== Embedded script =="
sed 's/^/    /' < "$WORK/autoexec.ipxe"
echo

# Build the five binaries. The bin- directories are iPXE's per-arch
# output dirs; the make targets pick the binary format and the
# embedded script in one go.
cd "$WORK/ipxe/src"
EMBED="$WORK/autoexec.ipxe"
TARGETS=(
    "bin/undionly.kpxe:undionly.kpxe"
    "bin/ipxe.pxe:ipxe.pxe"
    "bin-x86_64-efi/ipxe.efi:ipxe.efi"
    "bin-x86_64-efi/snponly.efi:snponly.efi"
    "bin-arm64-efi/ipxe.efi:ipxe-arm64.efi"
)

# arm64 builds need a cross-compiler. Detect and warn if missing
# rather than blowing up partway through.
if [ -z "$(command -v aarch64-linux-gnu-gcc 2>/dev/null)" ]; then
    echo "== aarch64-linux-gnu-gcc not found; skipping arm64 build =="
    echo "   (install gcc-aarch64-linux-gnu to include ipxe-arm64.efi)"
    # Drop the arm64 target.
    UNFILTERED=("${TARGETS[@]}")
    TARGETS=()
    for t in "${UNFILTERED[@]}"; do
        case "$t" in bin-arm64-efi/*) : ;; *) TARGETS+=("$t") ;; esac
    done
fi

install -d -m 0755 -o autodeploy -g autodeploy "$DATA_DIR/ipxe"
declare -a OK_BUILDS=()
declare -a FAILED_BUILDS=()
for spec in "${TARGETS[@]}"; do
    src="${spec%%:*}"
    dst="${spec##*:}"
    arch_dir="${src%%/*}"
    bin_name="${src##*/}"
    echo
    echo "== Building $src (-> $dst) =="
    case "$arch_dir" in
        bin)            CROSS=""  ;;
        bin-x86_64-efi) CROSS=""  ;;
        bin-arm64-efi)  CROSS="CROSS_COMPILE=aarch64-linux-gnu-" ;;
    esac
    log="/tmp/ipxe-build-${arch_dir}-${bin_name}.log"
    # shellcheck disable=SC2086
    if make $CROSS "$arch_dir/$bin_name" EMBED="$EMBED" >"$log" 2>&1; then
        install -m 0644 -o autodeploy -g autodeploy \
            "$arch_dir/$bin_name" "$DATA_DIR/ipxe/$dst"
        echo "    installed $DATA_DIR/ipxe/$dst"
        OK_BUILDS+=("$dst")
        rm -f "$log"
    else
        echo "  WARN: build failed for $src -- last 25 lines:" >&2
        tail -25 "$log" | sed 's/^/    /' >&2
        echo "  (full log: $log)" >&2
        FAILED_BUILDS+=("$dst")
    fi
done

echo
echo "== Done =="
if [ ${#OK_BUILDS[@]} -gt 0 ]; then
    echo "  built: ${OK_BUILDS[*]}"
fi
if [ ${#FAILED_BUILDS[@]} -gt 0 ]; then
    echo "  failed: ${FAILED_BUILDS[*]}" >&2
fi
ls -la "$DATA_DIR/ipxe/" | grep -E "\.(efi|kpxe|pxe)$"
echo
echo "PXE boot a target now; the embedded script will chain to:"
echo "  $URL/ipxe/boot.ipxe"
echo "No DHCP changes needed."
# Exit non-zero if NOTHING built so CI / wrapper scripts notice.
[ ${#OK_BUILDS[@]} -gt 0 ]

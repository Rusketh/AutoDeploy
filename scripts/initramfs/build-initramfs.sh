#!/usr/bin/env bash
# scripts/initramfs/build-initramfs.sh
#
# Build a minimal initramfs that boots straight into autodeploy-boot.
#
# Requirements on the build host:
#   - bash, cpio, gzip
#   - a Linux kernel image to ship alongside (vmlinuz) — see notes below
#   - the tools the Boot Client needs at runtime:
#         busybox or coreutils (cp, mkdir, mount, umount, reboot)
#         sgdisk (gptfdisk)
#         dosfstools (mkfs.fat)
#         ntfs-3g (mkfs.ntfs, mount.ntfs-3g)
#         wimlib (wimlib-imagex)
#   The script copies whichever it can find from the build host; missing
#   tools will be reported.
#
# Output: build/initrd.img (compressed cpio) and a list of what was bundled.
#
# Kernel: this script does NOT build a kernel. Supply your own vmlinuz that
# includes drivers for the target NIC and disk subsystem (most distro
# generic kernels are fine). Place it at build/vmlinuz before invoking
# the iPXE chainload.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BUILD="$ROOT/build"
ROOTFS="$BUILD/initramfs-root"
OUT="$BUILD/initrd.img"

rm -rf "$ROOTFS"
mkdir -p "$ROOTFS"/{bin,sbin,etc,proc,sys,dev,run,tmp,mnt,usr/bin,usr/sbin}

# Build the Boot Client static binary.
make -C "$ROOT/boot-client" build
install -m 0755 "$ROOT/boot-client/bin/autodeploy-boot" "$ROOTFS/sbin/autodeploy-boot"

# Pull in a tiny set of host tools so the imaging plan can run. We copy the
# binary and any dynamically-linked libraries it needs.
copy_with_libs() {
    local bin="$1"
    if [ ! -x "$bin" ]; then return 1; fi
    install -D -m 0755 "$bin" "$ROOTFS$bin"
    if command -v ldd >/dev/null 2>&1; then
        ldd "$bin" 2>/dev/null | awk '{print $3}' | grep -E '^/' | while read -r lib; do
            install -D -m 0755 "$lib" "$ROOTFS$lib"
        done
        # Loader.
        for loader in /lib64/ld-linux-x86-64.so.2 /lib/ld-linux.so.2; do
            [ -e "$loader" ] && install -D -m 0755 "$loader" "$ROOTFS$loader"
        done
    fi
}

missing=()
for tool in /bin/busybox /bin/sh /bin/mkdir /bin/mount /bin/umount /bin/cp \
            /sbin/sgdisk /sbin/mkfs.fat /sbin/mkfs.ntfs /sbin/reboot \
            /usr/bin/wimlib-imagex; do
    if ! copy_with_libs "$tool" 2>/dev/null; then
        missing+=("$tool")
    fi
done

# Minimal init that mounts /proc /sys, then execs autodeploy-boot.
cat > "$ROOTFS/init" <<'INIT'
#!/bin/sh
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sysfs /sys 2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null

# Kernel command line carries autodeploy.server=... and autodeploy.uuid=...
# from the iPXE script; convert to flags.
for arg in $(cat /proc/cmdline); do
    case "$arg" in
        autodeploy.server=*) SERVER="${arg#autodeploy.server=}" ;;
    esac
done

exec /sbin/autodeploy-boot --server "${SERVER:-}" menu
INIT
chmod 0755 "$ROOTFS/init"

# Pack.
cd "$ROOTFS"
find . -print0 | cpio --null --create --format=newc | gzip -9 > "$OUT"

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
if [ ${#missing[@]} -gt 0 ]; then
    echo "WARNING: missing tools (host may not have them):"
    for m in "${missing[@]}"; do echo "  - $m"; done
fi
echo
echo "Next steps:"
echo "  1. Place $OUT and your vmlinuz at AUTODEPLOY_DATA_DIR/ipxe/"
echo "     as autodeploy-initrd and autodeploy-kernel respectively."
echo "  2. Point your DHCP server's bootfile to http://<autodeploy>/ipxe/boot.ipxe"
echo "     for iPXE clients."

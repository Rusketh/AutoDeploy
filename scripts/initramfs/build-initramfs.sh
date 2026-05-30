#!/usr/bin/env bash
# scripts/initramfs/build-initramfs.sh
#
# Build a minimal initramfs that boots straight into autodeploy-boot.
#
# This script is what AutoDeploy's CI (.github/workflows/build-bootimage.yml)
# runs to produce the prebuilt `autodeploy-initrd` release asset, so most
# operators NEVER run it themselves -- they just fetch the prebuilt
# kernel + initrd (scripts/fetch-ipxe.sh / the installer do this). Run it
# by hand only if you want to customise the image.
#
# Requirements on the build host:
#   - bash, cpio, gzip
#   - Go toolchain (to compile the Boot Client)
#   - a Linux kernel whose MODULES are installed under
#     /lib/modules/<version>/ (so the image can drive the target's NIC
#     and disk). Defaults to the running kernel; override with
#     KERNEL_VERSION=<ver>.
#   - the tools the Boot Client shells out to at runtime:
#         busybox (sh + core utilities)
#         modprobe / depmod (kmod)        -- load NIC/disk drivers
#         sgdisk (gptfdisk)
#         mkfs.fat (dosfstools)
#         mkfs.ntfs (ntfs-3g)
#         wimlib-imagex (wimtools)
#   The script copies whichever it can find from the build host; missing
#   tools are reported.
#
# Output: build/initrd.img (compressed cpio) and a list of what was
# bundled. Pair it with the matching kernel image (the CI workflow ships
# /boot/vmlinuz-<version> as `autodeploy-kernel`).
#
# Env:
#   KERNEL_VERSION   kernel modules to bundle (default: $(uname -r))
#   OUT_DIR          where to write initrd.img (default: <repo>/build)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BUILD="${OUT_DIR:-$ROOT/build}"
ROOTFS="$BUILD/initramfs-root"
OUT="$BUILD/initrd.img"
KVER="${KERNEL_VERSION:-$(uname -r)}"
MODSRC="/lib/modules/$KVER"

rm -rf "$ROOTFS"
mkdir -p "$BUILD"
mkdir -p "$ROOTFS"/{bin,sbin,etc,proc,sys,dev,run,tmp,mnt,usr/bin,usr/sbin,lib,lib64}

# Build the Boot Client static binary.
make -C "$ROOT/boot-client" build
install -m 0755 "$ROOT/boot-client/bin/autodeploy-boot" "$ROOTFS/sbin/autodeploy-boot"

# Pull in a tiny set of host tools so the imaging plan can run. We copy
# the binary and any dynamically-linked libraries it needs.
copy_with_libs() {
    local bin="$1"
    if [ ! -x "$bin" ]; then return 1; fi
    install -D -m 0755 "$bin" "$ROOTFS$bin"
    if command -v ldd >/dev/null 2>&1; then
        ldd "$bin" 2>/dev/null | awk '{print $3}' | grep -E '^/' | while read -r lib; do
            install -D -m 0755 "$lib" "$ROOTFS$lib"
        done
        # Loaders.
        for loader in /lib64/ld-linux-x86-64.so.2 /lib/ld-linux.so.2 \
                      /lib/ld-linux-aarch64.so.1; do
            [ -e "$loader" ] && install -D -m 0755 "$loader" "$ROOTFS$loader"
        done
    fi
    return 0
}

missing=()
# busybox first -- it backstops any of the named tools below that the
# host happens to provide only as a busybox applet.
for tool in /bin/busybox \
            /bin/sh /bin/mkdir /bin/mount /bin/umount /bin/cp /bin/sleep /bin/cat \
            /sbin/modprobe /sbin/depmod /sbin/insmod \
            /sbin/sgdisk /sbin/mkfs.fat /sbin/mkfs.ntfs /sbin/reboot \
            /usr/bin/wimlib-imagex; do
    if ! copy_with_libs "$tool" 2>/dev/null; then
        missing+=("$tool")
    fi
done

# If busybox is present, symlink the core applets we rely on so the init
# script works even when the host didn't ship standalone binaries.
if [ -x "$ROOTFS/bin/busybox" ]; then
    for applet in sh mkdir mount umount cp sleep cat ls \
                  modprobe depmod insmod reboot; do
        [ -e "$ROOTFS/bin/$applet" ] || ln -sf busybox "$ROOTFS/bin/$applet"
    done
fi

# Bundle kernel modules so the image can actually see the target's NIC
# and disks. The Boot Client does its own DHCP in pure Go, but it can't
# do that until the NIC driver is loaded -- that's this section's job.
#
# We copy the whole modules tree (minus the build/source dev symlinks)
# and re-run depmod against the staged root so modprobe can resolve
# dependencies at boot. Copying everything trades initrd size for "it
# just works on whatever hardware shows up" -- the dominant concern for
# a bare-metal imaging tool.
if [ -d "$MODSRC" ]; then
    echo "== Bundling kernel modules for $KVER =="
    mkdir -p "$ROOTFS/lib/modules"
    cp -a "$MODSRC" "$ROOTFS/lib/modules/$KVER"
    # Drop the dev symlinks -- they point at kernel headers we don't ship.
    rm -f "$ROOTFS/lib/modules/$KVER/build" "$ROOTFS/lib/modules/$KVER/source"
    if command -v depmod >/dev/null 2>&1; then
        depmod -b "$ROOTFS" "$KVER" 2>/dev/null || \
            echo "  WARN: depmod failed; modprobe dependency resolution may be limited" >&2
    fi
else
    echo "  WARN: no modules found at $MODSRC -- the image will only work" >&2
    echo "        if the kernel has the target NIC/disk drivers built in." >&2
    echo "        Set KERNEL_VERSION to a kernel whose modules are installed." >&2
fi

# Minimal init: mount the virtual filesystems, load the drivers needed to
# reach the network and the disks, then hand off to autodeploy-boot.
cat > "$ROOTFS/init" <<'INIT'
#!/bin/sh
export PATH=/sbin:/usr/sbin:/bin:/usr/bin
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sysfs /sys 2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null

echo
echo "=== AutoDeploy Boot Client (initramfs) ==="

# Load the drivers the Boot Client needs to see the NIC and disks.
# modprobe silently ignores modules that are absent or already built in,
# so this same list is safe across hypervisors and bare metal.
for m in \
    hv_vmbus hv_netvsc hv_storvsc \
    virtio virtio_pci virtio_net virtio_blk virtio_scsi \
    e1000 e1000e igb igc ixgbe i40e tg3 r8169 atlantic \
    bnx2 bnx2x bnxt_en mlx4_en mlx5_core \
    ahci libahci ata_piix nvme nvme_core \
    sd_mod sr_mod usb_storage xhci_pci ehci_pci \
    vfat nls_cp437 nls_iso8859_1 ntfs3 fuse; do
    modprobe "$m" 2>/dev/null
done

# Synthetic NICs (Hyper-V, virtio) can take a moment to enumerate after
# the driver loads; give the link a beat before the Boot Client looks.
sleep 3

# Kernel command line carries autodeploy.server=... and autodeploy.uuid=...
# from the iPXE script; convert to flags.
SERVER=""
for arg in $(cat /proc/cmdline); do
    case "$arg" in
        autodeploy.server=*) SERVER="${arg#autodeploy.server=}" ;;
    esac
done

/sbin/autodeploy-boot --server "${SERVER:-}" menu

# If the Boot Client returns (error, or operator quit), don't panic as
# PID 1 -- drop to a shell so the screen stays up and is debuggable.
echo
echo "autodeploy-boot exited; dropping to a shell."
exec /bin/sh
INIT
chmod 0755 "$ROOTFS/init"

# Pack.
cd "$ROOTFS"
find . -print0 | cpio --null --create --format=newc 2>/dev/null | gzip -9 > "$OUT"

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
if [ ${#missing[@]} -gt 0 ]; then
    echo "WARNING: missing tools (host may not have them):"
    for m in "${missing[@]}"; do echo "  - $m"; done
fi
echo
echo "Next steps:"
echo "  1. Place $OUT and the matching vmlinuz at AUTODEPLOY_DATA_DIR/ipxe/"
echo "     as autodeploy-initrd and autodeploy-kernel respectively."
echo "     (Most operators skip this -- fetch-ipxe.sh / the installer pull"
echo "     the prebuilt kernel + initrd from the GitHub release.)"
echo "  2. Point your DHCP server's bootfile at one of the iPXE binaries;"
echo "     the server serves autoexec.ipxe and chains to /ipxe/boot.ipxe."

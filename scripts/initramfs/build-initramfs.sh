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
#         sgdisk (gptfdisk)        -- partition the target disk
#         mkfs.fat (dosfstools)    -- format the FAT32 boot partition
#         efibootmgr               -- register the boot partition w/ firmware
#         unzip                    -- expand driver packages onto the media
# AutoDeploy deploys by staging a bootable copy of the install media onto
# a single FAT32 partition (boot-the-media); the oversized install.wim is
# split into .swm parts server-side, so no NTFS/wimlib is needed here.
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
# Copy tools from KNOWN, explicit paths. This deliberately mirrors the
# long-proven layout: busybox + core applets under /bin, kmod
# (modprobe/depmod/insmod) and the imaging tools under /sbin. An earlier
# attempt to "resolve by name" pushed these into /usr/bin|/usr/sbin on a
# usr-merged build host, which broke the /bin/busybox applet links (kernel
# panic, no /bin/sh) and the kmod module-load path (Invalid ELF magic --
# the wrong loader ran). Explicit paths avoid all of that.
#
# busybox first -- it backstops any named tool the host provides only as a
# busybox applet.
for tool in /bin/busybox \
            /bin/sh /bin/mkdir /bin/mount /bin/umount /bin/cp /bin/sleep /bin/cat \
            /sbin/modprobe /sbin/depmod /sbin/insmod \
            /sbin/ip /sbin/udhcpc \
            /sbin/sgdisk /sbin/mkfs.fat /sbin/reboot \
            /usr/bin/unzip; do
    if ! copy_with_libs "$tool" 2>/dev/null; then
        missing+=("$tool")
    fi
done

# efibootmgr registers the staged boot partition with the firmware. It
# lives in /usr/sbin on Debian/Ubuntu but /sbin elsewhere, so try both.
if ! copy_with_libs /usr/sbin/efibootmgr 2>/dev/null \
   && ! copy_with_libs /sbin/efibootmgr 2>/dev/null; then
    missing+=("efibootmgr")
fi

# Safety net: if busybox somehow landed outside /bin (e.g. a host that
# only ships it under /usr/bin), put it at /bin/busybox so the applet
# symlinks below (and /bin/sh) resolve.
if [ ! -e "$ROOTFS/bin/busybox" ]; then
    for cand in usr/bin/busybox sbin/busybox usr/sbin/busybox; do
        if [ -e "$ROOTFS/$cand" ]; then
            install -D -m 0755 "$ROOTFS/$cand" "$ROOTFS/bin/busybox"
            break
        fi
    done
fi

# If busybox is present, symlink the core applets we rely on so the init
# script works even when the host didn't ship standalone binaries. ip and
# udhcpc are busybox applets on most build hosts; the symlinks make them
# available even when no standalone binary was found above.
if [ -x "$ROOTFS/bin/busybox" ]; then
    for applet in sh mkdir mount umount cp sleep cat ls grep basename sync \
                  unzip modprobe depmod insmod reboot \
                  ip ifconfig route udhcpc; do
        [ -e "$ROOTFS/bin/$applet" ] || ln -sf busybox "$ROOTFS/bin/$applet"
    done
fi

# Hard requirement: /init starts with #!/bin/sh, so /bin/sh MUST exist or
# the kernel panics at boot with "Failed to execute /init (error -2) ...
# No working init found". Guarantee it, and fail the BUILD loudly if we
# somehow can't -- a build error is far cheaper than a panic on the rig.
[ -e "$ROOTFS/bin/sh" ] || ln -sf busybox "$ROOTFS/bin/sh"
if [ ! -e "$ROOTFS/bin/sh" ]; then
    echo "FATAL: /bin/sh is missing from the initramfs; /init would not exec." >&2
    echo "       busybox was not bundled at $ROOTFS/bin/busybox." >&2
    exit 1
fi

# busybox udhcpc needs a "default.script" to apply the lease it gets
# (set the IP, routes, DNS). Without it udhcpc obtains a lease but never
# configures the interface. This is the minimal script from busybox's
# examples, trimmed to what the Boot Client needs (address + gateway +
# resolv.conf).
install -d -m 0755 "$ROOTFS/usr/share/udhcpc"
cat > "$ROOTFS/usr/share/udhcpc/default.script" <<'DHCPSCRIPT'
#!/bin/sh
# busybox udhcpc callback. $1 is the phase: deconfig | bound | renew.
RESOLV=/etc/resolv.conf
case "$1" in
    deconfig)
        ip addr flush dev "$interface" 2>/dev/null
        ip link set "$interface" up 2>/dev/null
        ;;
    bound|renew)
        ip addr add "$ip/${mask:-24}" dev "$interface" 2>/dev/null || \
            ifconfig "$interface" "$ip" netmask "${subnet:-255.255.255.0}" 2>/dev/null
        if [ -n "$router" ]; then
            ip route del default 2>/dev/null
            ip route add default via "$router" dev "$interface" 2>/dev/null || \
                route add default gw "$router" 2>/dev/null
        fi
        : > "$RESOLV"
        [ -n "$domain" ] && echo "search $domain" >> "$RESOLV"
        for d in $dns; do echo "nameserver $d" >> "$RESOLV"; done
        ;;
esac
exit 0
DHCPSCRIPT
chmod 0755 "$ROOTFS/usr/share/udhcpc/default.script"

# Bundle kernel modules so the image can actually see the target's NIC
# and disks. The driver has to be loaded before we can DHCP the NIC --
# that's what init does below; this section just ships the modules.
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
# efivarfs lets efibootmgr register the staged boot partition with the
# firmware. Absent/failing on legacy-BIOS hosts -- harmless there.
mount -t efivarfs efivarfs /sys/firmware/efi/efivars 2>/dev/null

echo
echo "=== AutoDeploy Boot Client (initramfs) ==="

# Load the drivers the Boot Client needs to see the NIC, disks, and the
# keyboard. modprobe silently ignores modules that are absent or already
# built in, so this same list is safe across hypervisors and bare metal.
for m in \
    hv_vmbus hv_netvsc hv_storvsc \
    virtio virtio_pci virtio_net virtio_blk virtio_scsi \
    e1000 e1000e igb igc ixgbe i40e tg3 r8169 atlantic \
    bnx2 bnx2x bnxt_en mlx4_en mlx5_core \
    ahci libahci ata_piix nvme nvme_core \
    sd_mod sr_mod usb_storage xhci_pci ehci_pci \
    vfat nls_cp437 nls_iso8859_1 ntfs3 fuse \
    hyperv_keyboard hid_hyperv \
    serio i8042 atkbd libps2 \
    usbhid hid hid_generic uhci_hcd ohci_hcd xhci_hcd ehci_hcd; do
    modprobe "$m" 2>/dev/null
done

# Synthetic NICs (Hyper-V, virtio) can take a moment to enumerate after
# the driver loads; give them a beat before we look for an interface.
sleep 2

# Bring up networking. The Boot Client speaks HTTP to the server but does
# NOT configure the interface itself, so init must DHCP a lease first --
# otherwise every server call fails and the menu never appears.
echo "Bringing up network..."
mkdir -p /etc/udhcpc
for dev in /sys/class/net/*; do
    [ -e "$dev" ] || continue
    ifc=${dev##*/}              # basename via shell builtin (no external dep)
    [ "$ifc" = "lo" ] && continue
    ip link set "$ifc" up 2>/dev/null
done
# DHCP on the first non-loopback link that comes up. -n: give up if no
# lease (don't background-retry forever); we then loop over interfaces.
GOTNET=0
for try in 1 2 3; do
    for dev in /sys/class/net/*; do
        [ -e "$dev" ] || continue
        ifc=${dev##*/}
        [ "$ifc" = "lo" ] && continue
        udhcpc -i "$ifc" -n -q -t 5 -T 3 \
            -s /usr/share/udhcpc/default.script >/dev/null 2>&1 && GOTNET=1
        # An interface with an IPv4 address is good enough to proceed.
        ip addr show "$ifc" 2>/dev/null | grep -q 'inet ' && GOTNET=1
    done
    [ "$GOTNET" = "1" ] && break
    echo "  no lease yet (attempt $try/3); retrying..."
    sleep 2
done
if [ "$GOTNET" != "1" ]; then
    echo "  WARNING: no DHCP lease obtained; the Boot Client will likely"
    echo "  fail to reach the server. Check the VM is on a bridged/external"
    echo "  switch that can reach AutoDeploy."
fi
echo "Network state:"
ip -4 addr show 2>/dev/null | grep -E 'inet |^[0-9]+:' || true

# Kernel command line carries autodeploy.server=... and autodeploy.uuid=...
# from the iPXE script; convert to flags.
SERVER=""
for arg in $(cat /proc/cmdline); do
    case "$arg" in
        autodeploy.server=*) SERVER="${arg#autodeploy.server=}" ;;
    esac
done

echo "Server: ${SERVER:-<unset>}"
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

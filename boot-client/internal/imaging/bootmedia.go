// bootmedia.go implements the "boot-the-media" disk layout: instead of
// applying a WIM to the target and hand-building a bootloader, AutoDeploy
// stages a bootable copy of the install media onto the target disk and
// lets the media's own installer run. This is OS-agnostic -- it works for
// any bootable ISO, which is what future Linux support needs.
//
// THE FAT32 PROBLEM
//
// UEFI firmware boots from a FAT32 EFI System Partition, but a modern
// Windows sources/install.wim (or install.esd) is frequently larger than
// FAT32's 4 GiB per-file limit, so the whole media cannot live on one
// FAT32 volume. We use the Rufus-proven split:
//
//   Partition 1  ESP    FAT32   ~1 GiB   bootloader + \EFI\ + \boot\ + small files
//   Partition 2  MEDIA  NTFS    rest     sources\ (the big WIM) + everything else
//
// The EFI bootloader on the ESP and Windows Setup both read across to the
// MEDIA partition, exactly as a Rufus "GPT/UEFI, NTFS" USB stick does.
//
// THIS FILE IS LAYOUT-ONLY. It builds and formats the two partitions and
// decides, per media file, which partition it lands on. Copying the media
// tree, dropping the answer file/drivers, setting the EFI boot entry and
// rebooting are the staging steps that build ON this and land in a later
// change. Keeping the layout isolated lets us validate the riskiest part
// (the split + routing) on its own, with tests, before anything
// destructive is wired into the live deploy path.
package imaging

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// fat32MaxFileBytes is FAT32's hard per-file ceiling (2^32 - 1). A file
// at or above this cannot be written to the ESP and must go on the NTFS
// MEDIA partition.
const fat32MaxFileBytes int64 = 4*1024*1024*1024 - 1

// espMinBytes is the floor for the FAT32 ESP. 1 GiB comfortably holds the
// boot files of any current Windows media (boot.wim included, which is
// well under 4 GiB) with room to spare. Kept modest so the MEDIA
// partition gets the rest of the disk for sources/.
const espMinBytes int64 = 1 * 1024 * 1024 * 1024

// BootMediaLayout describes the two-partition bootable-media target.
type BootMediaLayout struct {
	// Disk is the whole-disk device node, e.g. /dev/sda or /dev/nvme0n1.
	Disk string
	// ESPLabel / MediaLabel are the filesystem labels. Windows' EFI
	// bootloader finds sources\ by walking volumes, so stable labels make
	// the cross-partition reference predictable.
	ESPLabel   string
	MediaLabel string
}

// DefaultBootMediaLayout returns the standard layout for a disk.
func DefaultBootMediaLayout(disk string) BootMediaLayout {
	return BootMediaLayout{
		Disk:       disk,
		ESPLabel:   "ESP",
		MediaLabel: "MEDIA",
	}
}

// ESPPart / MediaPart return the partition device nodes (handling the
// nvme "p" suffix via the existing partName helper).
func (l BootMediaLayout) ESPPart() string   { return partName(l.Disk, 1) }
func (l BootMediaLayout) MediaPart() string { return partName(l.Disk, 2) }

// Build lays down the GPT, creates the FAT32 ESP + NTFS MEDIA partitions
// and formats both. It is destructive: it zaps any existing table first
// (the operator was warned -- a failed deploy may remove the previous
// OS). Sizing: a fixed ~1 GiB ESP, MEDIA takes the remainder.
//
// Build issues every action through the Runner so tests assert the exact
// command sequence and a dry run logs intent without touching disk.
func (l BootMediaLayout) Build(ctx context.Context, r Runner) error {
	if err := r.Exec(ctx, "sgdisk", "--zap-all", l.Disk); err != nil {
		return fmt.Errorf("zap %s: %w", l.Disk, err)
	}
	// 1 GiB ESP (type ef00), remainder MEDIA (type 0700 = basic data,
	// what Windows install media uses for its NTFS volume).
	if err := r.Exec(ctx, "sgdisk",
		"--new=1:0:+1024M", "--typecode=1:ef00", "--change-name=1:"+l.ESPLabel,
		"--new=2:0:0", "--typecode=2:0700", "--change-name=2:"+l.MediaLabel,
		l.Disk); err != nil {
		return fmt.Errorf("partition %s: %w", l.Disk, err)
	}
	if err := r.Exec(ctx, "mkfs.fat", "-F32", "-n", l.ESPLabel, l.ESPPart()); err != nil {
		return fmt.Errorf("mkfs.fat %s: %w", l.ESPPart(), err)
	}
	if err := r.Exec(ctx, "mkfs.ntfs", "-Q", "-L", l.MediaLabel, l.MediaPart()); err != nil {
		return fmt.Errorf("mkfs.ntfs %s: %w", l.MediaPart(), err)
	}
	return nil
}

// Target is which partition a given media file is staged onto.
type Target int

const (
	// TargetESP: small boot file -> FAT32 ESP.
	TargetESP Target = iota
	// TargetMedia: large file, or sources/ content -> NTFS MEDIA.
	TargetMedia
)

// RouteFile decides where one media file lands, given its media-relative
// path (forward-slashed, e.g. "sources/install.wim") and its size.
//
// Rules, in order:
//  1. Anything that does not fit on FAT32 -> MEDIA (the hard constraint).
//  2. The sources/ tree -> MEDIA. It holds install.wim/esd (the big one)
//     and boot.wim; keeping the whole directory on one volume means the
//     bootloader's relative references inside sources/ resolve on a
//     single filesystem, matching how install media is authored.
//  3. The EFI bootloader and boot/ files -> ESP, so the firmware finds a
//     bootable EFI binary on the FAT32 volume it actually boots from.
//  4. Everything else (small top-level files) -> ESP.
//
// This is deliberately a pure function of (path,size) so it is trivially
// testable and has no I/O.
func RouteFile(relPath string, size int64) Target {
	if size >= fat32MaxFileBytes {
		return TargetMedia
	}
	clean := strings.ToLower(path.Clean("/" + strings.ReplaceAll(relPath, "\\", "/")))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "sources" || strings.HasPrefix(clean, "sources/") {
		return TargetMedia
	}
	return TargetESP
}

// MountPoints holds the staging mount points under a work dir.
type MountPoints struct {
	ESP   string
	Media string
}

// Mount mounts both partitions under workDir/{esp,media}. The caller is
// responsible for unmounting (defer Unmount).
func (l BootMediaLayout) Mount(ctx context.Context, r Runner, workDir string) (MountPoints, error) {
	mp := MountPoints{
		ESP:   path.Join(workDir, "esp"),
		Media: path.Join(workDir, "media"),
	}
	if err := r.Exec(ctx, "mkdir", "-p", mp.ESP, mp.Media); err != nil {
		return mp, err
	}
	if err := r.Exec(ctx, "mount", l.ESPPart(), mp.ESP); err != nil {
		return mp, fmt.Errorf("mount esp: %w", err)
	}
	if err := r.Exec(ctx, "mount", l.MediaPart(), mp.Media); err != nil {
		_ = r.Exec(ctx, "umount", mp.ESP)
		return mp, fmt.Errorf("mount media: %w", err)
	}
	return mp, nil
}

// Unmount unmounts both partitions (best effort, media first).
func (l BootMediaLayout) Unmount(ctx context.Context, r Runner, mp MountPoints) {
	_ = r.Exec(ctx, "umount", mp.Media)
	_ = r.Exec(ctx, "umount", mp.ESP)
}

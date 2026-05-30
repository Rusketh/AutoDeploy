// Package imaging owns the on-disk steps of a deployment. AutoDeploy
// deploys by the BOOT-THE-MEDIA model: it stages a bootable copy of the
// install media onto the target and lets the media's own installer
// (Windows Setup) run -- it does NOT apply a WIM or hand-build a
// bootloader. This is OS-agnostic: the same steps boot any bootable ISO.
//
// The target disk gets a single FAT32 boot partition holding the whole
// media (an oversized install.wim was already split into <4 GiB .swm
// parts server-side at ISO ingest, so it fits FAT32 -- no NTSF/GRUB/shim
// needed). autounattend.xml is dropped at the media root where Setup
// auto-detects it; driver packages go under $WinPEDriver$\ where WinPE
// auto-loads them. The partition is registered with the firmware
// (efibootmgr) and the caller reboots into Setup.
//
// Real hardware work runs external tools (sgdisk, mkfs.fat, cp,
// efibootmgr) via os/exec. To keep this package testable without a
// target disk, every destructive step funnels through Runner.Exec, which
// is a Recorder in tests and a real exec.Command at runtime.
//
// FAIL-SAFE: if any step returns an error, StageMedia does not reboot.
// The caller logs the failure and exits 1; the firmware then falls back
// to the normal boot device.
package imaging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner is the boundary between this package and the host operating system.
// Real deployments use OSRunner; tests use Recorder.
type Runner interface {
	Exec(ctx context.Context, name string, args ...string) error
}

// OSRunner runs commands via os/exec, streaming stdout/stderr to log.
type OSRunner struct {
	Log *slog.Logger
	// DryRun reports what would happen without actually executing.
	DryRun bool
}

// Exec implements Runner.
func (r *OSRunner) Exec(ctx context.Context, name string, args ...string) error {
	if r.Log != nil {
		r.Log.Info("imaging.exec",
			slog.String("actor", "boot-client"),
			slog.String("target", name),
			slog.String("args", fmt.Sprintf("%q", args)),
			slog.Bool("dry_run", r.DryRun),
		)
	}
	if r.DryRun {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdoutWriter{r.Log}
	cmd.Stderr = stderrWriter{r.Log}
	return cmd.Run()
}

type stdoutWriter struct{ log *slog.Logger }

func (w stdoutWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Info("imaging.exec.stdout", slog.String("line", string(p)))
	}
	return len(p), nil
}

type stderrWriter struct{ log *slog.Logger }

func (w stderrWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Warn("imaging.exec.stderr", slog.String("line", string(p)))
	}
	return len(p), nil
}

// MediaPlan describes one boot-media deployment. The media tree is
// downloaded directly onto the mounted boot partition by the caller
// (between PreparePartition and FinalizeMedia), so there is no local
// staging dir. UnattendPath is the generated autounattend.xml;
// DriverPaths are downloaded driver payloads. TargetDisk is the device
// node (e.g. /dev/sda); MediaBytes sizes the boot partition.
type MediaPlan struct {
	TargetDisk   string
	MediaBytes   int64
	UnattendPath string
	DriverPaths  []string
	// WorkDir is a scratch directory for the mount point.
	WorkDir string
}

const (
	// bootPartMinMiB floors the FAT32 boot partition so it always fits a
	// full Windows media set even if MediaBytes is unknown (0).
	bootPartMinMiB int64 = 7168 // 7 GiB
	// bootPartMarginNum/Den add headroom over MediaBytes (here +25%).
	bootPartMarginNum = 5
	bootPartMarginDen = 4
)

// bootPartitionMiB sizes the FAT32 boot partition from the media size
// plus margin, never below the floor.
func bootPartitionMiB(mediaBytes int64) int64 {
	miB := (mediaBytes / (1024 * 1024)) * bootPartMarginNum / bootPartMarginDen
	if miB < bootPartMinMiB {
		return bootPartMinMiB
	}
	return miB
}

// PreparePartition zaps the disk, creates and formats the single FAT32
// boot partition, mounts it, and returns the mount path. The caller then
// downloads the media tree DIRECTLY onto the returned path (not via a
// RAM-backed staging dir -- a multi-GB Windows media tree would exhaust
// the initramfs tmpfs) and finishes with FinalizeMedia.
//
// Disk layout: a single FAT32 boot partition (type ef00) at the END of
// the disk, leaving the front free for Windows Setup to install into.
// The answer file is responsible for installing into that free space
// without wiping the boot partition; post-install cleanup (delete the
// boot partition, extend C:) is the agent's job.
func PreparePartition(ctx context.Context, plan MediaPlan, r Runner) (mountPath string, err error) {
	d := plan.TargetDisk
	sizeMiB := bootPartitionMiB(plan.MediaBytes)

	if err := r.Exec(ctx, "sgdisk", "--zap-all", d); err != nil {
		return "", fmt.Errorf("zap %s: %w", d, err)
	}
	// Negative start places the partition at the end of the disk; the
	// remaining space at the front is left free for the OS install.
	if err := r.Exec(ctx, "sgdisk",
		fmt.Sprintf("--new=1:-%dM:0", sizeMiB),
		"--typecode=1:ef00", "--change-name=1:ADBOOT", d); err != nil {
		return "", fmt.Errorf("partition %s: %w", d, err)
	}
	boot := partName(d, 1)
	if err := r.Exec(ctx, "mkfs.fat", "-F32", "-n", "ADBOOT", boot); err != nil {
		return "", fmt.Errorf("mkfs.fat %s: %w", boot, err)
	}

	mount := filepath.Join(plan.WorkDir, "media")
	if err := r.Exec(ctx, "mkdir", "-p", mount); err != nil {
		return "", fmt.Errorf("prepare mount point: %w", err)
	}
	if err := r.Exec(ctx, "mount", boot, mount); err != nil {
		return "", fmt.Errorf("mount %s: %w", boot, err)
	}
	return mount, nil
}

// FinalizeMedia drops the answer file and drivers onto the already-mounted
// boot partition, flushes, unmounts, and registers the partition with the
// firmware. Call it after the media tree has been downloaded onto
// mountPath. It does NOT reboot -- the caller decides based on whether
// anything failed.
func FinalizeMedia(ctx context.Context, plan MediaPlan, r Runner, mountPath string) error {
	if err := placeUnattend(ctx, plan, r, mountPath); err != nil {
		return fmt.Errorf("place unattend: %w", err)
	}
	if err := stageDrivers(ctx, plan, r, mountPath); err != nil {
		return fmt.Errorf("stage drivers: %w", err)
	}
	// Flush before unmount so writeback of several GB of media doesn't race
	// the unmount.
	_ = r.Exec(ctx, "sync")
	if err := r.Exec(ctx, "umount", mountPath); err != nil {
		return fmt.Errorf("umount %s: %w", mountPath, err)
	}

	// Register the boot partition so the firmware boots Windows Setup.
	// The \EFI\BOOT\BOOTX64.EFI fallback path is also present on the
	// media, so even firmware that ignores added NVRAM entries can boot it.
	if err := r.Exec(ctx, "efibootmgr", "--create",
		"--disk", plan.TargetDisk, "--part", "1",
		"--loader", `\EFI\BOOT\BOOTX64.EFI`,
		"--label", "AutoDeploy Setup"); err != nil {
		return fmt.Errorf("register boot entry: %w", err)
	}
	return nil
}

// placeUnattend drops the answer file at the media root as
// autounattend.xml, where Windows Setup auto-detects it on the boot
// media. (This replaces the old capture/apply behaviour of writing
// Windows\Panther\unattend.xml into an applied image.)
func placeUnattend(ctx context.Context, plan MediaPlan, r Runner, mount string) error {
	if plan.UnattendPath == "" {
		return nil
	}
	return r.Exec(ctx, "cp", plan.UnattendPath, filepath.Join(mount, "autounattend.xml"))
}

// stageDrivers places each downloaded driver package under
// <mount>\$WinPEDriver$\<name>\, the folder WinPE auto-loads during
// Setup. Each blob is a zip (SCCM-style export the server validates) or,
// as a legacy fallback, a single opaque file.
func stageDrivers(ctx context.Context, plan MediaPlan, r Runner, mount string) error {
	for _, p := range plan.DriverPaths {
		base := filepath.Base(p)
		dst := filepath.Join(mount, "$WinPEDriver$", strings.TrimSuffix(base, filepath.Ext(base)))
		if err := r.Exec(ctx, "mkdir", "-p", dst); err != nil {
			return err
		}
		if isZip(p) {
			if err := r.Exec(ctx, "unzip", "-o", "-q", p, "-d", dst); err != nil {
				return err
			}
		} else {
			if err := r.Exec(ctx, "cp", p, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func partName(disk string, n int) string {
	// /dev/sda -> /dev/sda{n}; /dev/nvme0n1 -> /dev/nvme0n1p{n}
	last := disk[len(disk)-1]
	if last >= '0' && last <= '9' {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// isZip is a cheap header sniff: PK\x03\x04 is the local file header
// magic. Empty file or unreadable -> false.
func isZip(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [4]byte
	if _, err := f.Read(hdr[:]); err != nil {
		return false
	}
	return hdr[0] == 'P' && hdr[1] == 'K' && hdr[2] == 0x03 && hdr[3] == 0x04
}

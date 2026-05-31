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
	// Output runs a read-only command and returns its stdout. Used to read
	// the current efibootmgr entries so stale ones can be pruned.
	Output(ctx context.Context, name string, args ...string) (string, error)
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

// Output runs a read-only command and returns its stdout. Unlike Exec it
// runs even under DryRun -- listing state is harmless and lets the dry run
// log what it would prune.
func (r *OSRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	if r.Log != nil {
		r.Log.Info("imaging.exec.output",
			slog.String("actor", "boot-client"),
			slog.String("target", name),
			slog.String("args", fmt.Sprintf("%q", args)))
	}
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
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
	// AgentPath is the AutoDeploy agent binary to inject into the
	// installed OS via the media's $OEM$ tree (empty = no agent).
	// SetupCompletePath is the generated SetupComplete.cmd that installs
	// it post-Setup. Both empty = deploy without an agent.
	AgentPath         string
	SetupCompletePath string
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
	//
	// Type 0700 (Microsoft basic data), NOT ef00 (EFI System Partition):
	// an ESP is hidden by Windows and never gets a drive letter, so
	// Windows Setup won't scan it for sources\install.swm and dead-ends at
	// "a media driver is missing". A basic-data FAT32 partition gets a
	// letter in WinPE, so Setup finds the install source. It still boots:
	// RegisterBootEntry adds an explicit UEFI boot entry that loads
	// \EFI\BOOT\BOOTX64.EFI by partition GUID, which firmware honours
	// regardless of partition type (ESP type only gates *automatic*
	// removable-media fallback discovery).
	if err := r.Exec(ctx, "sgdisk",
		fmt.Sprintf("--new=1:-%dM:0", sizeMiB),
		"--typecode=1:0700", "--change-name=1:ADBOOT", d); err != nil {
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
// boot partition, flushes, and unmounts. Call it after the media tree has
// been downloaded onto mountPath. Registering the firmware boot entry is a
// separate, best-effort step (RegisterBootEntry).
func FinalizeMedia(ctx context.Context, plan MediaPlan, r Runner, mountPath string) error {
	if err := placeUnattend(ctx, plan, r, mountPath); err != nil {
		return fmt.Errorf("place unattend: %w", err)
	}
	if err := stageDrivers(ctx, plan, r, mountPath); err != nil {
		return fmt.Errorf("stage drivers: %w", err)
	}
	if err := stageAgent(ctx, plan, r, mountPath); err != nil {
		return fmt.Errorf("stage agent: %w", err)
	}
	// Flush before unmount so writeback of several GB of media doesn't race
	// the unmount.
	_ = r.Exec(ctx, "sync")
	if err := r.Exec(ctx, "umount", mountPath); err != nil {
		return fmt.Errorf("umount %s: %w", mountPath, err)
	}
	return nil
}

// bootEntryLabel is the UEFI boot-entry description AutoDeploy creates and
// prunes. Kept as one constant so create and cleanup never drift.
const bootEntryLabel = "AutoDeploy Setup"

// RegisterBootEntry adds a UEFI boot entry pointing at the staged
// partition's bootloader and makes it first in BootOrder. It is
// best-effort: the media also carries the firmware fallback path
// \EFI\BOOT\BOOTX64.EFI, so a machine whose firmware honours that can
// still boot the staged media even if this fails. The caller logs a
// warning and reboots anyway rather than stranding a fully-staged disk.
//
// It first prunes any AutoDeploy entries left by previous deploys --
// without this the firmware boot list accumulates a BOOTX64.EFI entry on
// every run.
func RegisterBootEntry(ctx context.Context, plan MediaPlan, r Runner) error {
	if out, err := r.Output(ctx, "efibootmgr"); err == nil {
		for _, num := range parseAutoDeployBootNums(out) {
			// -b NNNN -B selects and deletes that boot entry. Best-effort:
			// a stale entry we can't remove shouldn't block the new one.
			_ = r.Exec(ctx, "efibootmgr", "-b", num, "-B")
		}
	}
	return r.Exec(ctx, "efibootmgr", "--create",
		"--disk", plan.TargetDisk, "--part", "1",
		"--loader", `\EFI\BOOT\BOOTX64.EFI`,
		"--label", bootEntryLabel)
}

// parseAutoDeployBootNums extracts the 4-hex bootnums of every existing
// AutoDeploy entry from efibootmgr's listing, e.g. a line
// "Boot0003* AutoDeploy Setup\tHD(...)" yields "0003".
func parseAutoDeployBootNums(efibootmgrOutput string) []string {
	var nums []string
	for _, line := range strings.Split(efibootmgrOutput, "\n") {
		rest, ok := strings.CutPrefix(line, "Boot")
		if !ok || len(rest) < 4 {
			continue
		}
		num := rest[:4]
		if !isHex4(num) {
			continue
		}
		// After the 4-digit bootnum comes an optional '*' (active flag)
		// then a space then the description.
		desc := strings.TrimSpace(strings.TrimPrefix(rest[4:], "*"))
		if strings.HasPrefix(desc, bootEntryLabel) {
			nums = append(nums, num)
		}
	}
	return nums
}

func isHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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

// stageAgent injects the AutoDeploy agent and its SetupComplete.cmd into
// the installed OS using Windows Setup's $OEM$ mechanism. Files under
// sources\$OEM$\$$\ are copied to %WINDIR% (C:\Windows) during install:
//
//	sources\$OEM$\$$\AutoDeploy\autodeploy-agent.exe -> C:\Windows\AutoDeploy\...
//	sources\$OEM$\$$\Setup\Scripts\SetupComplete.cmd -> C:\Windows\Setup\Scripts\...
//
// Windows runs SetupComplete.cmd as SYSTEM at the end of Setup; it copies
// the agent into Program Files and registers an on-start task. This
// replaces the old (broken) first-logon \\Windows\... bootstrap, which
// used a bad UNC path, an unset %AUTODEPLOY_SERVER%, and a binary that was
// never actually placed on the machine.
func stageAgent(ctx context.Context, plan MediaPlan, r Runner, mount string) error {
	if plan.AgentPath == "" {
		return nil
	}
	// $$ maps to %WINDIR%. The literal "$OEM$"/"$$" names are passed as
	// exec args (no shell), so the dollar signs are not expanded.
	oemWin := filepath.Join(mount, "sources", "$OEM$", "$$")
	agentDir := filepath.Join(oemWin, "AutoDeploy")
	if err := r.Exec(ctx, "mkdir", "-p", agentDir); err != nil {
		return err
	}
	if err := r.Exec(ctx, "cp", plan.AgentPath, filepath.Join(agentDir, "autodeploy-agent.exe")); err != nil {
		return err
	}
	if plan.SetupCompletePath != "" {
		scriptsDir := filepath.Join(oemWin, "Setup", "Scripts")
		if err := r.Exec(ctx, "mkdir", "-p", scriptsDir); err != nil {
			return err
		}
		if err := r.Exec(ctx, "cp", plan.SetupCompletePath, filepath.Join(scriptsDir, "SetupComplete.cmd")); err != nil {
			return err
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

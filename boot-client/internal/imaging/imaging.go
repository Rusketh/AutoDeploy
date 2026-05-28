// Package imaging owns the on-disk steps of a deployment: partition the
// target disk, fetch and apply the WIM/ESD, inject driver packages, place
// the unattend, and ask the firmware to reboot.
//
// Real hardware work runs external tools (sgdisk, mkfs.fat, wimlib-imagex)
// via os/exec. To keep this package testable without a target disk, every
// destructive step funnels through Runner.Exec, which can be replaced with
// a recorder in tests and a real exec.Command at runtime.
//
// FAIL-SAFE: if any step returns an error, Apply does not reboot. The
// caller (cmd/autodeploy-boot) logs the failure and exits 1; the firmware
// then falls back to the normal boot device.
package imaging

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
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

// Plan describes one deployment to apply. WIMPath and UnattendPath are local
// paths the caller has already downloaded; DriverPaths is the list of driver
// payload paths to inject. TargetDisk is the device node (e.g. /dev/sda).
type Plan struct {
	TargetDisk    string
	WIMPath       string
	WIMImageIndex int // wimlib image index; 1 for a single-image install.wim
	UnattendPath  string
	DriverPaths   []string
	// WorkDir is a scratch directory the imaging code may use for mount
	// points and intermediate files.
	WorkDir string
}

// Apply partitions the disk, applies the WIM, injects drivers and writes
// the unattend. It does NOT reboot — the caller decides whether to do so
// based on whether anything failed.
func Apply(ctx context.Context, plan Plan, r Runner) error {
	if plan.WIMImageIndex == 0 {
		plan.WIMImageIndex = 1
	}
	if err := partition(ctx, plan, r); err != nil {
		return fmt.Errorf("partition: %w", err)
	}
	if err := applyWIM(ctx, plan, r); err != nil {
		return fmt.Errorf("apply wim: %w", err)
	}
	if err := injectDrivers(ctx, plan, r); err != nil {
		return fmt.Errorf("inject drivers: %w", err)
	}
	if err := placeUnattend(ctx, plan, r); err != nil {
		return fmt.Errorf("place unattend: %w", err)
	}
	return nil
}

// partition lays down a UEFI GPT layout: 100 MiB ESP + remainder NTFS for
// Windows. Resilient choice: clear any existing partition table first so
// re-imaging a previously deployed machine is reliable.
func partition(ctx context.Context, plan Plan, r Runner) error {
	d := plan.TargetDisk
	if err := r.Exec(ctx, "sgdisk", "--zap-all", d); err != nil {
		return err
	}
	if err := r.Exec(ctx, "sgdisk",
		"--new=1:0:+100M", "--typecode=1:ef00", "--change-name=1:ESP",
		"--new=2:0:0", "--typecode=2:0700", "--change-name=2:Windows",
		d); err != nil {
		return err
	}
	if err := r.Exec(ctx, "mkfs.fat", "-F32", "-n", "ESP", partName(d, 1)); err != nil {
		return err
	}
	if err := r.Exec(ctx, "mkfs.ntfs", "-Q", "-L", "Windows", partName(d, 2)); err != nil {
		return err
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

func applyWIM(ctx context.Context, plan Plan, r Runner) error {
	// Mount the Windows partition under WorkDir/win, apply WIM, unmount.
	mount := filepath.Join(plan.WorkDir, "win")
	if err := r.Exec(ctx, "mkdir", "-p", mount); err != nil {
		return err
	}
	if err := r.Exec(ctx, "mount", partName(plan.TargetDisk, 2), mount); err != nil {
		return err
	}
	defer func() { _ = r.Exec(ctx, "umount", mount) }()
	return r.Exec(ctx, "wimlib-imagex", "apply",
		plan.WIMPath, fmt.Sprint(plan.WIMImageIndex), mount)
}

func injectDrivers(ctx context.Context, plan Plan, r Runner) error {
	if len(plan.DriverPaths) == 0 {
		return nil
	}
	mount := filepath.Join(plan.WorkDir, "win")
	// Each driver payload is added with dism --add-driver. The driver
	// payload format is finalised in Phase 4; here we trust the server's
	// matching and inject every package we were handed.
	for _, p := range plan.DriverPaths {
		if err := r.Exec(ctx, "wimlib-imagex", "update",
			plan.WIMPath, fmt.Sprint(plan.WIMImageIndex),
			"--command=add "+p+" /Windows/INF/AutoDeploy/"); err != nil {
			return err
		}
		_ = mount
	}
	return nil
}

func placeUnattend(ctx context.Context, plan Plan, r Runner) error {
	if plan.UnattendPath == "" {
		return nil
	}
	mount := filepath.Join(plan.WorkDir, "win")
	dst := filepath.Join(mount, "Windows", "Panther", "unattend.xml")
	if err := r.Exec(ctx, "mkdir", "-p", filepath.Dir(dst)); err != nil {
		return err
	}
	// File copy goes through the runner so dry-run logs the intent and the
	// recorder can assert on it. cp is present in any sane initramfs.
	return r.Exec(ctx, "cp", plan.UnattendPath, dst)
}

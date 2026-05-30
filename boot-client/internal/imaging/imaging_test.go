package imaging

import (
	"context"
	"strings"
	"testing"
)

func TestStageMediaIssuesExpectedSteps(t *testing.T) {
	rec := &Recorder{}
	plan := MediaPlan{
		TargetDisk:   "/dev/sda",
		MediaBytes:   6 * 1024 * 1024 * 1024, // 6 GiB media
		UnattendPath: "/tmp/unattend.xml",
		DriverPaths:  []string{"/tmp/drv1", "/tmp/drv2"}, // non-zip -> cp path
		WorkDir:      "/tmp/work",
	}
	// Prepare partition first; the caller streams media onto the returned
	// mount path (not exercised here), then finalizes.
	mount, err := PreparePartition(context.Background(), plan, rec)
	if err != nil {
		t.Fatal(err)
	}
	if mount != "/tmp/work/media" {
		t.Fatalf("mount path = %q", mount)
	}
	if err := FinalizeMedia(context.Background(), plan, rec, mount); err != nil {
		t.Fatal(err)
	}
	mustContain := []string{
		"sgdisk --zap-all /dev/sda",
		// Single FAT32 boot partition at the END of the disk (negative
		// start), leaving the front free for Windows Setup.
		"--new=1:-7680M:0 --typecode=1:ef00 --change-name=1:ADBOOT /dev/sda",
		"mkfs.fat -F32 -n ADBOOT /dev/sda1",
		// Partition is mounted BEFORE the media download (which streams
		// straight onto it, avoiding a RAM staging copy).
		"mount /dev/sda1 /tmp/work/media",
		// Answer file at the media root where Setup auto-detects it.
		"cp /tmp/unattend.xml /tmp/work/media/autounattend.xml",
		// Drivers under $WinPEDriver$ (cp path for non-zip fixtures).
		"mkdir -p /tmp/work/media/$WinPEDriver$/drv1",
		"cp /tmp/drv1 /tmp/work/media/$WinPEDriver$/drv1",
		"mkdir -p /tmp/work/media/$WinPEDriver$/drv2",
		"cp /tmp/drv2 /tmp/work/media/$WinPEDriver$/drv2",
		"sync",
		"umount /tmp/work/media",
		// Firmware boot entry for Windows Setup.
		`efibootmgr --create --disk /dev/sda --part 1 --loader \EFI\BOOT\BOOTX64.EFI --label AutoDeploy Setup`,
	}
	for _, want := range mustContain {
		if !rec.Has(want) {
			t.Errorf("missing call containing %q\n%s", want, rec.Dump())
		}
	}
	// No RAM staging copy, and the old capture/apply model must be gone.
	for _, gone := range []string{"media-src", "cp -a", "wimlib-imagex", "mkfs.ntfs", "Windows/Panther"} {
		if rec.Has(gone) {
			t.Errorf("unexpected leftover %q\n%s", gone, rec.Dump())
		}
	}
}

func TestBootPartitionSizing(t *testing.T) {
	// Below floor -> floor.
	if got := bootPartitionMiB(1 * 1024 * 1024 * 1024); got != bootPartMinMiB {
		t.Errorf("small media sizing = %d, want floor %d", got, bootPartMinMiB)
	}
	// 6 GiB media + 25% margin = 7680 MiB.
	if got := bootPartitionMiB(6 * 1024 * 1024 * 1024); got != 7680 {
		t.Errorf("6GiB media sizing = %d, want 7680", got)
	}
	// Unknown size (0) -> floor.
	if got := bootPartitionMiB(0); got != bootPartMinMiB {
		t.Errorf("zero media sizing = %d, want floor", got)
	}
}

func TestPartitionNamingNVMe(t *testing.T) {
	if got := partName("/dev/nvme0n1", 1); got != "/dev/nvme0n1p1" {
		t.Errorf("partName(nvme) = %q", got)
	}
	if got := partName("/dev/sda", 1); got != "/dev/sda1" {
		t.Errorf("partName(sda) = %q", got)
	}
}

func TestPreparePartitionStopsOnFirstError(t *testing.T) {
	rec := &failAfter{Recorder: &Recorder{}, failAt: 1}
	plan := MediaPlan{TargetDisk: "/dev/sda", WorkDir: "/tmp/work"}
	_, err := PreparePartition(context.Background(), plan, rec)
	if err == nil || !strings.Contains(err.Error(), "zap") {
		t.Fatalf("expected zap error, got %v", err)
	}
}

// failAfter is a Runner that fails on the Nth call (1-indexed).
type failAfter struct {
	*Recorder
	failAt int
	called int
}

func (f *failAfter) Exec(ctx context.Context, name string, args ...string) error {
	f.called++
	if f.called == f.failAt {
		return errSentinel
	}
	return f.Recorder.Exec(ctx, name, args...)
}

type sentinelError string

func (s sentinelError) Error() string { return string(s) }

const errSentinel = sentinelError("forced failure")

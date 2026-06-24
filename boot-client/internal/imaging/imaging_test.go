package imaging

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOSRunnerRunsUnderUTF8Locale verifies imaging subprocesses inherit a UTF-8
// locale. mkfs.fat iconv-converts the FAT label from codepage 850; under the
// initramfs C/POSIX locale (charset ANSI_X3.4) iconv_open fails, so OSRunner
// must export LC_ALL=C.UTF-8 to every command it runs.
func TestOSRunnerRunsUnderUTF8Locale(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	out, err := (&OSRunner{}).Output(context.Background(), "sh", "-c", `printf %s "$LC_ALL"`)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if out != "C.UTF-8" {
		t.Errorf("LC_ALL in imaging subprocess = %q, want C.UTF-8", out)
	}
}

func TestStageMediaIssuesExpectedSteps(t *testing.T) {
	rec := &Recorder{}
	plan := MediaPlan{
		TargetDisk:         "/dev/sda",
		MediaBytes:         6 * 1024 * 1024 * 1024, // 6 GiB media
		UnattendPath:       "/tmp/unattend.xml",
		Drivers:            []MediaDriver{{BlobPath: "/tmp/drv1"}, {BlobPath: "/tmp/drv2"}}, // non-zip -> OS-bucket cp path
		AgentPath:          "/tmp/payload-agent.exe",
		CredProviderPath:   "/tmp/payload-credprovider.dll",
		SetupCompletePath:  "/tmp/SetupComplete.cmd",
		CallbackScriptPath: "/tmp/adcb.ps1",
		WorkDir:            "/tmp/work",
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
	if err := RegisterBootEntry(context.Background(), plan, rec); err != nil {
		t.Fatal(err)
	}
	mustContain := []string{
		"sgdisk --zap-all /dev/sda",
		// Single FAT32 boot partition at the END of the disk (negative
		// start), leaving the front free for Windows Setup. Type 0700
		// (basic data) NOT ef00 (ESP) so Windows gives it a drive letter
		// and Setup can find sources\install.swm on it.
		"--new=1:-7680M:0 --typecode=1:0700 --change-name=1:ADBOOT /dev/sda",
		"mkfs.fat -F32 -n ADBOOT /dev/sda1",
		// Partition is mounted BEFORE the media download (which streams
		// straight onto it, avoiding a RAM staging copy).
		"mount /dev/sda1 /tmp/work/media",
		// Answer file at the media root where Setup auto-detects it.
		"cp /tmp/unattend.xml /tmp/work/media/autounattend.xml",
		// Non-zip drivers go to the OS bucket (sources\$OEM$\$1\Drivers =
		// C:\Drivers); they no longer touch WinPE (the Win11 24H2 abort fix).
		"mkdir -p /tmp/work/media/sources/$OEM$/$1/Drivers/drv1",
		"cp /tmp/drv1 /tmp/work/media/sources/$OEM$/$1/Drivers/drv1",
		"mkdir -p /tmp/work/media/sources/$OEM$/$1/Drivers/drv2",
		"cp /tmp/drv2 /tmp/work/media/sources/$OEM$/$1/Drivers/drv2",
		// Agent + SetupComplete.cmd injected via the $OEM$ tree ($$=%WINDIR%).
		"mkdir -p /tmp/work/media/sources/$OEM$/$$/AutoDeploy",
		"cp /tmp/payload-agent.exe /tmp/work/media/sources/$OEM$/$$/AutoDeploy/autodeploy-agent.exe",
		// Setup-lock credential provider DLL injected next to the agent.
		"cp /tmp/payload-credprovider.dll /tmp/work/media/sources/$OEM$/$$/AutoDeploy/autodeploy-credprovider.dll",
		"mkdir -p /tmp/work/media/sources/$OEM$/$$/Setup/Scripts",
		"cp /tmp/SetupComplete.cmd /tmp/work/media/sources/$OEM$/$$/Setup/Scripts/SetupComplete.cmd",
		// Install-status milestone reporter staged into the same Scripts dir.
		"cp /tmp/adcb.ps1 /tmp/work/media/sources/$OEM$/$$/Setup/Scripts/adcb.ps1",
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
	// No RAM staging copy, the old capture/apply model must be gone, and with
	// no zip (boot-critical) drivers in this plan, nothing touches WinPE.
	for _, gone := range []string{"media-src", "cp -a", "wimlib-imagex", "mkfs.ntfs", "Windows/Panther", "$WinPEDriver$"} {
		if rec.Has(gone) {
			t.Errorf("unexpected leftover %q\n%s", gone, rec.Dump())
		}
	}
}

func TestRegisterBootEntryPrunesStaleAutoDeployEntries(t *testing.T) {
	// Two prior AutoDeploy entries plus the firmware's own should be
	// pruned down to nothing before the fresh one is created -- otherwise
	// the boot list grows a BOOTX64.EFI entry every deploy.
	rec := &Recorder{OutputResult: `BootCurrent: 0001
Timeout: 0 seconds
BootOrder: 0003,0004,0000,0001
Boot0000* Windows Boot Manager	HD(1,GPT,...)
Boot0001* UEFI Network	BBS(Network,...)
Boot0003* AutoDeploy Setup	HD(1,GPT,...)\EFI\BOOT\BOOTX64.EFI
Boot0004* AutoDeploy Setup	HD(1,GPT,...)\EFI\BOOT\BOOTX64.EFI
`}
	plan := MediaPlan{TargetDisk: "/dev/sda"}
	if err := RegisterBootEntry(context.Background(), plan, rec); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"efibootmgr -b 0003 -B",
		"efibootmgr -b 0004 -B",
		`efibootmgr --create --disk /dev/sda --part 1 --loader \EFI\BOOT\BOOTX64.EFI --label AutoDeploy Setup`,
	} {
		if !rec.Has(want) {
			t.Errorf("missing %q\n%s", want, rec.Dump())
		}
	}
	// Must NOT delete unrelated entries.
	for _, gone := range []string{"-b 0000 -B", "-b 0001 -B"} {
		if rec.Has(gone) {
			t.Errorf("pruned an unrelated boot entry: %q\n%s", gone, rec.Dump())
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
	// Empty disk must not panic (defensive: callers resolve a real device).
	if got := partName("", 1); got != "" {
		t.Errorf("partName(\"\") = %q, want \"\"", got)
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

// writeTestZip writes a real zip on disk so isZip() sees it and stageDrivers
// takes the unzip+split path (the Recorder records commands but never runs
// them, so the unzip output need not exist).
func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, werr := zw.Create(name)
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr := w.Write([]byte(body)); werr != nil {
			t.Fatal(werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestStageDriversSplitsBootCriticalToWinPE locks in the Win11 24H2 fix: the
// whole package is staged into the OS $OEM$\$1\Drivers tree (installed online,
// where a bad INF is non-fatal), while ONLY the server-flagged boot-critical
// subtree is additionally copied into $WinPEDriver$. A Bluetooth driver must
// never reach WinPE -- that is what aborted Setup with 0xE0000219.
func TestStageDriversSplitsBootCriticalToWinPE(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "payload-driver-pkg.zip")
	writeTestZip(t, zipPath, map[string]string{
		"Storage/iaStorAC/iaStorAC.inf": "[Version]\nClass=SCSIAdapter\n",
		"Bluetooth/ibt/oem43.inf":       "[Version]\nClass=Bluetooth\n",
	})
	rec := &Recorder{}
	plan := MediaPlan{
		Drivers: []MediaDriver{{
			BlobPath:  zipPath,
			WinPEDirs: []string{"Storage/iaStorAC"}, // the server's verdict
		}},
		WorkDir: "/tmp/work",
	}
	if err := stageDrivers(context.Background(), plan, rec, "/mnt"); err != nil {
		t.Fatal(err)
	}
	const name = "payload-driver-pkg" // base minus .zip
	want := []string{
		// Whole package -> OS bucket (C:\Drivers via $OEM$\$1), installed online.
		"unzip -o -q " + zipPath + " -d /mnt/sources/$OEM$/$1/Drivers/" + name,
		// Boot-critical storage subtree ALSO copied into WinPE so Setup sees the disk.
		"mkdir -p /mnt/$WinPEDriver$/" + name + "/Storage",
		"cp -r /mnt/sources/$OEM$/$1/Drivers/" + name + "/Storage/iaStorAC /mnt/$WinPEDriver$/" + name + "/Storage/iaStorAC",
	}
	for _, w := range want {
		if !rec.Has(w) {
			t.Errorf("missing call containing %q\n%s", w, rec.Dump())
		}
	}
	// The Bluetooth driver must NEVER be copied into WinPE.
	if rec.Has("$WinPEDriver$/" + name + "/Bluetooth") {
		t.Errorf("Bluetooth driver leaked into $WinPEDriver$ -- this is the 0xE0000219 abort\n%s", rec.Dump())
	}
}

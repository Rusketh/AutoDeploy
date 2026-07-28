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
		// 6 GiB media + 512 MiB staged-extras reserve, +25% margin = 8320 MiB
		// (the two non-zip test "drivers" don't exist on disk, so contribute 0).
		"--new=1:-8320M:0 --typecode=1:0700 --change-name=1:ADBOOT /dev/sda",
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
	// Two prior AutoDeploy entries should be pruned before the fresh one is
	// created -- otherwise the boot list grows a BOOTX64.EFI entry every deploy.
	// The first snapshot (pre-create) drives pruning; the second (post-create,
	// verbose) drives BootNext/BootOrder.
	rec := &Recorder{OutputResults: []string{
		`BootCurrent: 0001
BootOrder: 0003,0004,0000,0001
Boot0000* Windows Boot Manager	HD(1,GPT,...)
Boot0001* UEFI Network	BBS(Network,...)
Boot0003* AutoDeploy Setup	HD(1,GPT,...)\EFI\BOOT\BOOTX64.EFI
Boot0004* AutoDeploy Setup	HD(1,GPT,...)\EFI\BOOT\BOOTX64.EFI
`,
		`BootCurrent: 0001
BootOrder: 0005,0000,0001
Boot0000* Windows Boot Manager	HD(1,GPT,...)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0001* UEFI Network	PciRoot(0x0)/Pci(0x1f,0x6)/MAC(001122334455,0)/IPv4(0.0.0.0)
Boot0005* AutoDeploy Setup	HD(1,GPT,...)/File(\EFI\BOOT\BOOTX64.EFI)
`,
	}}
	plan := MediaPlan{TargetDisk: "/dev/sda"}
	if err := RegisterBootEntry(context.Background(), plan, rec); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"efibootmgr -b 0003 -B",
		"efibootmgr -b 0004 -B",
		`efibootmgr --create --disk /dev/sda --part 1 --loader \EFI\BOOT\BOOTX64.EFI --label AutoDeploy Setup`,
		// One-shot boot into the freshly created entry.
		"efibootmgr --bootnext 0005",
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

// TestRegisterBootEntryDemotesNetworkBoot locks in the fix for the iPXE reboot
// loop: after staging, the network/PXE entry -- the firmware's primary, since
// the machine PXE-booted to get here -- must be pushed to the END of BootOrder
// so the next Windows Setup reboot boots the disk, not the network.
func TestRegisterBootEntryDemotesNetworkBoot(t *testing.T) {
	rec := &Recorder{OutputResults: []string{
		// Pre-create: network (0002) is the primary boot option.
		`BootCurrent: 0002
BootOrder: 0002,0000
Boot0000* Windows Boot Manager	HD(...)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0002* UEFI: IPv4 Realtek PCIe GbE	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(0011223344,0)/IPv4(0.0.0.0)
`,
		// Post-create: efibootmgr prepended the new entry (0007).
		`BootCurrent: 0002
BootOrder: 0007,0002,0000
Boot0000* Windows Boot Manager	HD(...)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0002* UEFI: IPv4 Realtek PCIe GbE	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(0011223344,0)/IPv4(0.0.0.0)
Boot0007* AutoDeploy Setup	HD(...)/File(\EFI\BOOT\BOOTX64.EFI)
`,
	}}
	if err := RegisterBootEntry(context.Background(), MediaPlan{TargetDisk: "/dev/sda"}, rec); err != nil {
		t.Fatal(err)
	}
	// Both the network entry (0002) AND our own staged-media entry (0007) move to
	// the end; Windows Boot Manager (0000) stays ahead so Setup's inter-phase
	// reboot boots Windows, not the network or the staged media.
	if !rec.Has("efibootmgr -o 0000,0007,0002") {
		t.Errorf("network/staged entries not demoted below Windows Boot Manager\n%s", rec.Dump())
	}
	if !rec.Has("efibootmgr --bootnext 0007") {
		t.Errorf("BootNext not pointed at the staged media entry\n%s", rec.Dump())
	}
}

// TestRegisterBootEntryDemotesOwnEntryBelowWindows locks in the fix for the
// ADBOOT install loop: efibootmgr --create prepends our entry, so on firmware
// that doesn't reorder for the freshly created Windows Boot Manager, Setup's
// mid-install reboot re-enters the staged media. Our own entry must be demoted
// below Windows Boot Manager in BootOrder.
func TestRegisterBootEntryDemotesOwnEntryBelowWindows(t *testing.T) {
	rec := &Recorder{OutputResults: []string{
		// Pre-create: a Windows Boot Manager from a prior install is present.
		`BootCurrent: 0002
BootOrder: 0002,0000
Boot0000* Windows Boot Manager	HD(...)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0002* UEFI: IPv4 Realtek PCIe GbE	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(0011223344,0)/IPv4(0.0.0.0)
`,
		// Post-create: efibootmgr prepended the new entry (0007) to BootOrder.
		`BootCurrent: 0002
BootOrder: 0007,0002,0000
Boot0000* Windows Boot Manager	HD(...)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0002* UEFI: IPv4 Realtek PCIe GbE	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(0011223344,0)/IPv4(0.0.0.0)
Boot0007* AutoDeploy Setup	HD(...)/File(\EFI\BOOT\BOOTX64.EFI)
`,
	}}
	if err := RegisterBootEntry(context.Background(), MediaPlan{TargetDisk: "/dev/sda"}, rec); err != nil {
		t.Fatal(err)
	}
	order := lastBootOrder(t, rec)
	winIdx, ourIdx := indexOf(order, "0000"), indexOf(order, "0007")
	if winIdx < 0 || ourIdx < 0 {
		t.Fatalf("expected both Windows Boot Manager and AutoDeploy entries in BootOrder, got %v\n%s", order, rec.Dump())
	}
	if ourIdx < winIdx {
		t.Errorf("AutoDeploy Setup (0007) is ahead of Windows Boot Manager (0000) in %v -- the ADBOOT loop is not fixed\n%s", order, rec.Dump())
	}
}

// lastBootOrder returns the bootnums from the final `efibootmgr -o` the code
// issued, or fails the test if none was recorded.
func lastBootOrder(t *testing.T, rec *Recorder) []string {
	t.Helper()
	const prefix = "efibootmgr -o "
	for i := len(rec.Calls) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(rec.Calls[i], prefix); ok {
			return strings.Split(rest, ",")
		}
	}
	t.Fatalf("no `efibootmgr -o` command recorded\n%s", rec.Dump())
	return nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// TestBootWindowsBootManager locks in the mid-install loop-breaker: pointing the
// firmware at the installed Windows boot manager sets BootNext to it AND moves
// it to the front of BootOrder with the network entry demoted to the end.
func TestBootWindowsBootManager(t *testing.T) {
	out := `BootCurrent: 0002
BootOrder: 0002,0007,0000
Boot0000* Windows Boot Manager	HD(1,GPT,abc)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0002* UEFI: PXE IPv4 Realtek	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(001122334455,0)/IPv4(0.0.0.0)
Boot0007* AutoDeploy Setup	HD(1,GPT,def)/File(\EFI\BOOT\BOOTX64.EFI)
`
	if !HasWindowsBootManager(out) {
		t.Fatal("HasWindowsBootManager = false, want true")
	}
	rec := &Recorder{}
	ok, err := BootWindowsBootManager(context.Background(), rec, out)
	if err != nil || !ok {
		t.Fatalf("BootWindowsBootManager ok=%v err=%v", ok, err)
	}
	// BootNext -> Windows Boot Manager (0000); BootOrder = WinBootMgr first,
	// network (0002) last.
	if !rec.Has("efibootmgr --bootnext 0000") {
		t.Errorf("BootNext not set to Windows Boot Manager\n%s", rec.Dump())
	}
	if !rec.Has("efibootmgr -o 0000,0007,0002") {
		t.Errorf("BootOrder not reordered (want 0000,0007,0002)\n%s", rec.Dump())
	}
}

// TestBootWindowsBootManagerAbsent: no Windows Boot Manager entry -> no-op.
func TestBootWindowsBootManagerAbsent(t *testing.T) {
	out := "BootOrder: 0001\nBoot0001* UEFI PXE\tMAC(001122334455,0)\n"
	if HasWindowsBootManager(out) {
		t.Fatal("HasWindowsBootManager = true on a listing without one")
	}
	rec := &Recorder{}
	ok, err := BootWindowsBootManager(context.Background(), rec, out)
	if err != nil || ok {
		t.Fatalf("BootWindowsBootManager on absent entry: ok=%v err=%v", ok, err)
	}
	if len(rec.Calls) != 0 {
		t.Errorf("expected no efibootmgr calls, got\n%s", rec.Dump())
	}
}

func TestPromoteToFront(t *testing.T) {
	// Present -> moved to front, order otherwise preserved.
	got := promoteToFront([]string{"0002", "0007", "0000"}, "0000")
	if strings.Join(got, ",") != "0000,0002,0007" {
		t.Errorf("promoteToFront(present) = %v", got)
	}
	// Absent -> prepended.
	got = promoteToFront([]string{"0002", "0007"}, "0000")
	if strings.Join(got, ",") != "0000,0002,0007" {
		t.Errorf("promoteToFront(absent) = %v", got)
	}
	// Empty num -> unchanged.
	got = promoteToFront([]string{"0002"}, "")
	if strings.Join(got, ",") != "0002" {
		t.Errorf("promoteToFront(empty) = %v", got)
	}
}

func TestParseNetworkBootNums(t *testing.T) {
	out := `BootOrder: 0000,0001,0002,0003,0004,0005
Boot0000* Windows Boot Manager	HD(1,GPT,abc)/File(\EFI\Microsoft\Boot\bootmgfw.efi)
Boot0001* AutoDeploy Setup	HD(1,GPT,def)/File(\EFI\BOOT\BOOTX64.EFI)
Boot0002* UEFI: PXE IPv4 Intel	PciRoot(0x0)/Pci(0x1f,0x6)/MAC(aabbccddeeff,0)/IPv4(0.0.0.0)
Boot0003* UEFI: PXE IPv6 Intel	PciRoot(0x0)/Pci(0x1f,0x6)/MAC(aabbccddeeff,0)/IPv6(::)
Boot0004* Onboard NIC	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(112233445566,0)
Boot0005* UEFI HTTPs Boot	PciRoot(0x0)/Pci(0x1c,0x0)/MAC(112233445566,0)/IPv4(1.2.3.4)/Uri(https://x/y)
`
	got := parseNetworkBootNums(out)
	want := []string{"0002", "0003", "0004", "0005"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parseNetworkBootNums = %v, want %v", got, want)
	}
	// Windows Boot Manager and AutoDeploy Setup must never be flagged network.
	for _, n := range got {
		if n == "0000" || n == "0001" {
			t.Errorf("misclassified disk entry %s as network\n%v", n, got)
		}
	}
}

func TestParseBootOrderAndDemote(t *testing.T) {
	out := "Timeout: 1\nBootOrder: 0002,0000,0001\nBoot0000* x\n"
	if got := parseBootOrder(out); strings.Join(got, ",") != "0002,0000,0001" {
		t.Errorf("parseBootOrder = %v", got)
	}
	if got := parseBootOrder("no order here"); got != nil {
		t.Errorf("parseBootOrder(none) = %v, want nil", got)
	}
	// Demote moves matching nums to the end, preserving order; absent nums are
	// ignored; both groups keep their relative order.
	got := demoteToEnd([]string{"0007", "0002", "0000", "0003"}, []string{"0002", "0003", "9999"})
	if strings.Join(got, ",") != "0007,0000,0002,0003" {
		t.Errorf("demoteToEnd = %v, want [0007 0000 0002 0003]", got)
	}
	if demoteToEnd(nil, []string{"0001"}) != nil {
		t.Error("demoteToEnd(nil, …) should be nil")
	}
}

func TestParseBootNumByLabel(t *testing.T) {
	out := `BootOrder: 0000,0005
Boot0000* Windows Boot Manager	HD(...)
Boot0005* AutoDeploy Setup	HD(...)/File(\EFI\BOOT\BOOTX64.EFI)
`
	if got := parseBootNumByLabel(out, bootEntryLabel); got != "0005" {
		t.Errorf("parseBootNumByLabel = %q, want 0005", got)
	}
	if got := parseBootNumByLabel(out, "Nonexistent"); got != "" {
		t.Errorf("parseBootNumByLabel(absent) = %q, want \"\"", got)
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

// TestDriverStagedBytesCountsExtractedPlusWinPEDuplicate verifies the partition
// sizing accounts for the driver footprint: every package counts at its full
// EXTRACTED (uncompressed) size, and the boot-critical subtree counts a SECOND
// time because stageDrivers copies it into $WinPEDriver$. This is what keeps a
// driver-heavy image from overflowing the FAT32 partition (the "No space left
// on device" unzip failure).
func TestDriverStagedBytesCountsExtractedPlusWinPEDuplicate(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pkg.zip")
	storage := strings.Repeat("S", 4096)  // boot-critical -> counted twice
	bluetooth := strings.Repeat("B", 2048) // OS-bucket only -> counted once
	writeTestZip(t, zipPath, map[string]string{
		"Storage/iaStorAC/iaStorAC.sys": storage,
		"Bluetooth/ibt/oem43.sys":       bluetooth,
	})
	d := MediaDriver{BlobPath: zipPath, WinPEDirs: []string{"Storage/iaStorAC"}}
	// OS bucket = both files (4096+2048); $WinPEDriver$ = the storage file again.
	want := int64(4096+2048) + int64(4096)
	if got := oneDriverStagedBytes(d); got != want {
		t.Errorf("oneDriverStagedBytes = %d, want %d", got, want)
	}
}

// TestDriverStagedBytesWholePackageWinPE checks a package the server flagged
// whole-boot-critical (WinPEDirs ["."]) is counted twice in full.
func TestDriverStagedBytesWholePackageWinPE(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "raid.zip")
	body := strings.Repeat("R", 8192)
	writeTestZip(t, zipPath, map[string]string{"raid.sys": body})
	d := MediaDriver{BlobPath: zipPath, WinPEDirs: []string{"."}}
	if got, want := oneDriverStagedBytes(d), int64(8192*2); got != want {
		t.Errorf("oneDriverStagedBytes(whole) = %d, want %d", got, want)
	}
}

// TestDriverStagedBytesNonZipAndMissing covers the fallbacks: a legacy opaque
// (non-zip) blob counts at its on-disk size, and a missing blob contributes 0
// rather than erroring the sizing.
func TestDriverStagedBytesNonZipAndMissing(t *testing.T) {
	dir := t.TempDir()
	opaque := filepath.Join(dir, "legacy.bin")
	if err := os.WriteFile(opaque, make([]byte, 1234), 0o644); err != nil {
		t.Fatal(err)
	}
	got := driverStagedBytes([]MediaDriver{
		{BlobPath: opaque},
		{BlobPath: filepath.Join(dir, "does-not-exist")},
	})
	if got != 1234 {
		t.Errorf("driverStagedBytes(non-zip + missing) = %d, want 1234", got)
	}
}

// writeBigTestZip writes a zip whose single entry has the given UNCOMPRESSED
// size, streamed in small chunks so the test never allocates the whole payload
// in memory (the repeated byte deflates to almost nothing on disk; only the
// central-directory uncompressed size matters to driverStagedBytes).
func writeBigTestZip(t *testing.T, path, entry string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20) // 1 MiB of zero bytes
	for remaining := size; remaining > 0; {
		n := int64(len(chunk))
		if n > remaining {
			n = remaining
		}
		if _, err := w.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		remaining -= n
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPreparePartitionSizesForDrivers locks in the fix end-to-end: a large
// driver zip in the plan must enlarge the boot partition past what the media
// size alone would yield, so the later unzip has room.
func TestPreparePartitionSizesForDrivers(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "big-drivers.zip")
	writeBigTestZip(t, zipPath, "Storage/big.sys", 5*1024*1024*1024) // 5 GiB extracted
	plan := MediaPlan{
		TargetDisk: "/dev/sda",
		MediaBytes: 6 * 1024 * 1024 * 1024, // 6 GiB media -> 7680 MiB on its own
		Drivers:    []MediaDriver{{BlobPath: zipPath}},
		WorkDir:    "/tmp/work",
	}
	rec := &Recorder{}
	if _, err := PreparePartition(context.Background(), plan, rec); err != nil {
		t.Fatal(err)
	}
	// 6 GiB media + 512 MiB extras alone sizes to 8320 MiB; the 5 GiB of
	// drivers must push it well past that: (6 GiB + 5 GiB + 512 MiB) * 5/4 =
	// 14720 MiB.
	if rec.Has("--new=1:-8320M:0") {
		t.Errorf("partition sized for media only, ignoring drivers\n%s", rec.Dump())
	}
	if !rec.Has("--new=1:-14720M:0") {
		t.Errorf("partition not sized for media+drivers (want 14720M)\n%s", rec.Dump())
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

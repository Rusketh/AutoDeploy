package payload

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBuildXorrisoArgs_DualBoot(t *testing.T) {
	args := buildXorrisoArgs(ISOSpec{
		OutPath:     "/out/image.iso",
		VolumeLabel: "AUTODEPLOY",
		Trees:       []string{"/media", "/overlay"},
		BIOSBootImg: "boot/etfsboot.com",
		UEFIBootImg: "efi/microsoft/boot/efisys.bin",
	})
	joined := strings.Join(args, " ")

	// mkisofs emulation with the media features a Windows tree needs.
	for _, want := range []string{
		"-as mkisofs", "-iso-level 3", "-J -joliet-long", "-R", "-V AUTODEPLOY",
		"-b boot/etfsboot.com", "-no-emul-boot", "-boot-load-size 8",
		"-eltorito-alt-boot", "-e efi/microsoft/boot/efisys.bin",
		"-o /out/image.iso", "-graft-points",
		"/=/media", "/=/overlay",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n got: %s", want, joined)
		}
	}
	// xorriso's mkisofs emulation rejects -udf outright (it aborts with exit 5),
	// and the media is pre-split for FAT32, so UDF must never be requested.
	if strings.Contains(joined, "-udf") {
		t.Errorf("must not request -udf (unsupported by xorriso -as mkisofs): %s", joined)
	}
	// -boot-info-table patches bytes 8..63 of the BIOS boot image in the ISO;
	// for Windows' etfsboot.com that overwrites its code and BIOS boot fails.
	if strings.Contains(joined, "-boot-info-table") {
		t.Errorf("must not pass -boot-info-table (corrupts etfsboot.com): %s", joined)
	}
	// The overlay tree must be grafted AFTER the media tree so it wins on a
	// path conflict (the injected autounattend.xml overrides any in the media).
	if strings.Index(joined, "/=/media") > strings.Index(joined, "/=/overlay") {
		t.Errorf("overlay tree must be grafted after the media tree: %s", joined)
	}
}

func TestBuildXorrisoArgs_NoBootImages(t *testing.T) {
	args := buildXorrisoArgs(ISOSpec{
		OutPath: "/out/image.iso", VolumeLabel: "X", Trees: []string{"/media"},
	})
	joined := strings.Join(args, " ")
	// With no boot images, no El Torito flags are emitted — the ISO still
	// carries a valid file layout (Rufus boots via efi\boot\bootx64.efi).
	for _, absent := range []string{"-b ", "-e ", "-eltorito-alt-boot"} {
		if strings.Contains(joined, absent) {
			t.Errorf("unexpected boot flag %q in %s", absent, joined)
		}
	}
}

func TestFindBootImages(t *testing.T) {
	root := t.TempDir()
	// Upper-case dirs, as real Windows media commonly carries. The returned
	// path MUST preserve this real casing, because xorriso's -b/-e lookup is
	// case-sensitive — returning the lower-case candidate would fail the build.
	mustWrite(t, filepath.Join(root, "BOOT", "ETFSBOOT.COM"), "x")
	mustWrite(t, filepath.Join(root, "EFI", "MICROSOFT", "BOOT", "EFISYS.BIN"), "x")

	bios, uefi := FindBootImages(root)
	if bios != "BOOT/ETFSBOOT.COM" {
		t.Errorf("bios = %q, want BOOT/ETFSBOOT.COM (real casing)", bios)
	}
	if uefi != "EFI/MICROSOFT/BOOT/EFISYS.BIN" {
		t.Errorf("uefi = %q, want EFI/MICROSOFT/BOOT/EFISYS.BIN (real casing)", uefi)
	}

	// A tree with neither returns empties (ExportImageISO then refuses).
	b2, u2 := FindBootImages(t.TempDir())
	if b2 != "" || u2 != "" {
		t.Errorf("empty tree returned boot images: %q %q", b2, u2)
	}
}

func TestFindBootImages_PrefersNoPrompt(t *testing.T) {
	// With both UEFI images present, the no-prompt one wins: efisys.bin waits
	// for "Press any key" and an unattended VM / BMC boot would skip Setup.
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "efi", "microsoft", "boot", "efisys.bin"), "p")
	mustWrite(t, filepath.Join(root, "efi", "microsoft", "boot", "efisys_noprompt.bin"), "n")
	if _, uefi := FindBootImages(root); uefi != "efi/microsoft/boot/efisys_noprompt.bin" {
		t.Errorf("uefi = %q, want efisys_noprompt.bin", uefi)
	}
	// Only the prompting image: still used.
	root2 := t.TempDir()
	mustWrite(t, filepath.Join(root2, "EFI", "Microsoft", "Boot", "efisys.bin"), "p")
	if _, uefi := FindBootImages(root2); uefi != "EFI/Microsoft/Boot/efisys.bin" {
		t.Errorf("uefi = %q, want EFI/Microsoft/Boot/efisys.bin", uefi)
	}
}

// recordingRunner captures the argv AuthorISO would run.
type recordingRunner struct {
	name string
	args []string
}

func (r *recordingRunner) Exec(_ context.Context, name string, args ...string) error {
	r.name = name
	r.args = args
	return nil
}

func TestAuthorISO_RunsBuilder(t *testing.T) {
	media := t.TempDir()
	overlay := t.TempDir()
	out := filepath.Join(t.TempDir(), "sub", "image.iso") // parent created by AuthorISO
	rr := &recordingRunner{}
	if err := AuthorISO(context.Background(), ISOSpec{
		OutPath: out, VolumeLabel: "AD", Trees: []string{media, overlay},
	}, rr); err != nil {
		t.Fatalf("AuthorISO: %v", err)
	}
	if rr.name != isoBuilderBin {
		t.Errorf("ran %q, want %q", rr.name, isoBuilderBin)
	}
	if _, err := os.Stat(filepath.Dir(out)); err != nil {
		t.Errorf("output dir not created: %v", err)
	}
}

func TestAuthorISO_RejectsMissingTree(t *testing.T) {
	err := AuthorISO(context.Background(), ISOSpec{
		OutPath: filepath.Join(t.TempDir(), "o.iso"),
		Trees:   []string{filepath.Join(t.TempDir(), "does-not-exist")},
	}, &recordingRunner{})
	if err == nil {
		t.Fatal("expected error for a missing source tree")
	}
}

// TestAuthorISO_RealXorriso drives the REAL xorriso (skipped when it isn't
// installed) against a tree shaped like real Windows media — upper-case boot
// images and the literal $OEM$ / $WinPEDriver$ overlay dirs — so it exercises
// the exact argv the export builds. This is the regression guard for the two
// bugs that made a real export fail: `-udf` (rejected by mkisofs emulation) and
// a lower-cased boot-image path (xorriso's -b/-e lookup is case-sensitive).
func TestAuthorISO_RealXorriso(t *testing.T) {
	if !ISOBuilderAvailable() {
		t.Skip("xorriso not installed")
	}
	media := t.TempDir()
	mustWrite(t, filepath.Join(media, "sources", "boot.wim"), "wim")
	mustWrite(t, filepath.Join(media, "sources", "install.swm"), "swm")
	// Real boot images are a few KiB. The BIOS image carries recognisable,
	// non-repeating bytes so the extracted copy can be compared exactly: any
	// in-ISO patching (-boot-info-table rewrites bytes 8..63) is caught.
	etfs := recognisableBytes("ETFSBOOT", 4096)
	mustWrite(t, filepath.Join(media, "BOOT", "ETFSBOOT.COM"), string(etfs))
	mustWrite(t, filepath.Join(media, "EFI", "MICROSOFT", "BOOT", "EFISYS.BIN"), strings.Repeat("u", 4096))
	noprompt := recognisableBytes("NOPROMPT", 8192)
	mustWrite(t, filepath.Join(media, "EFI", "MICROSOFT", "BOOT", "EFISYS_NOPROMPT.BIN"), string(noprompt))
	// Real media carries the UEFI fallback loader (this is also how Rufus makes
	// the USB bootable); include it so the tree matches a real export.
	mustWrite(t, filepath.Join(media, "EFI", "BOOT", "BOOTX64.EFI"), strings.Repeat("e", 4096))

	overlay := t.TempDir()
	mustWrite(t, filepath.Join(overlay, "autounattend.xml"), "<xml/>")
	mustWrite(t, filepath.Join(overlay, "sources", "$OEM$", "$$", "Setup", "Scripts", "SetupComplete.cmd"), "@echo off\r\n")
	mustWrite(t, filepath.Join(overlay, "$WinPEDriver$", "intel_rst", "oem.inf"), "inf")

	bios, uefi := FindBootImages(media)
	if bios == "" || uefi == "" {
		t.Fatalf("boot images not found: bios=%q uefi=%q", bios, uefi)
	}
	out := filepath.Join(t.TempDir(), "image.iso")
	if err := AuthorISO(context.Background(), ISOSpec{
		OutPath:     out,
		VolumeLabel: "AUTODEPLOY",
		Trees:       []string{media, overlay},
		BIOSBootImg: bios,
		UEFIBootImg: uefi,
	}, &OSISORunner{}); err != nil {
		t.Fatalf("AuthorISO with real xorriso: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("output ISO missing: %v", err)
	}
	if fi.Size() < 32*1024 {
		t.Errorf("ISO suspiciously small: %d bytes", fi.Size())
	}
	if _, ok := FinishedExport(out); !ok {
		t.Error("FinishedExport rejects a real xorriso ISO")
	}

	// The El Torito catalog has a BIOS and a UEFI no-emulation entry pointing
	// at the real-cased images (the no-prompt UEFI loader preferred).
	report := xorrisoOut(t, "-indev", out, "-report_el_torito", "plain")
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`El Torito boot img :\s+1\s+BIOS\s+y\s+none`),
		regexp.MustCompile(`El Torito boot img :\s+2\s+UEFI\s+y\s+none`),
		regexp.MustCompile(`El Torito img path :\s+1\s+/BOOT/ETFSBOOT\.COM`),
		regexp.MustCompile(`El Torito img path :\s+2\s+/EFI/MICROSOFT/BOOT/EFISYS_NOPROMPT\.BIN`),
	} {
		if !want.MatchString(report) {
			t.Errorf("El Torito report missing %s\n%s", want, report)
		}
	}
	// etfsboot.com in the ISO must be byte-identical to the source: no
	// boot-info-table (or anything else) patched into it.
	got := xorrisoExtract(t, out, "/BOOT/ETFSBOOT.COM")
	if !bytes.Equal(got, etfs) {
		t.Errorf("etfsboot.com was modified inside the ISO (first diff at byte %d)", firstDiff(got, etfs))
	}
	if got := xorrisoExtract(t, out, "/EFI/MICROSOFT/BOOT/EFISYS_NOPROMPT.BIN"); !bytes.Equal(got, noprompt) {
		t.Error("efisys_noprompt.bin was modified inside the ISO")
	}
}

// recognisableBytes returns n bytes of tag-prefixed, position-dependent data,
// so a byte-level comparison pinpoints any patched region.
func recognisableBytes(tag string, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	copy(b, tag)
	return b
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// xorrisoOut runs the real xorriso and returns its stdout.
func xorrisoOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(isoBuilderBin, args...).Output()
	if err != nil {
		t.Fatalf("xorriso %v: %v", args, err)
	}
	return string(out)
}

// xorrisoExtract copies isoPath (an absolute path inside the ISO) out of the
// image and returns its bytes.
func xorrisoExtract(t *testing.T, iso, isoPath string) []byte {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "x")
	if err := exec.Command(isoBuilderBin, "-osirrox", "on", "-indev", iso, "-extract", isoPath, dst).Run(); err != nil {
		t.Fatalf("extract %s: %v", isoPath, err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// xorrisoRootNames lists the names directly under dir inside the ISO.
func xorrisoRootNames(t *testing.T, iso, dir string) []string {
	t.Helper()
	var names []string
	for _, l := range strings.Split(xorrisoOut(t, "-indev", iso, "-ls", dir), "\n") {
		l = strings.TrimSpace(l)
		if len(l) >= 2 && l[0] == '\'' && l[len(l)-1] == '\'' {
			names = append(names, l[1:len(l)-1])
		}
	}
	return names
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

package payload

import (
	"context"
	"os"
	"path/filepath"
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
		"-b boot/etfsboot.com", "-no-emul-boot", "-boot-info-table",
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

	// A tree with neither returns empties (still exportable, non-bootable ISO).
	b2, u2 := FindBootImages(t.TempDir())
	if b2 != "" || u2 != "" {
		t.Errorf("empty tree returned boot images: %q %q", b2, u2)
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
	// Real boot images are a few KiB; -boot-info-table patches a 56-byte table
	// into the BIOS image, so a too-small stub makes xorriso MISHAP.
	mustWrite(t, filepath.Join(media, "BOOT", "ETFSBOOT.COM"), strings.Repeat("b", 4096))
	mustWrite(t, filepath.Join(media, "EFI", "MICROSOFT", "BOOT", "EFISYS.BIN"), strings.Repeat("u", 4096))
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

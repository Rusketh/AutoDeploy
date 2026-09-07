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
		"-as mkisofs", "-iso-level 3", "-udf", "-V AUTODEPLOY",
		"-b boot/etfsboot.com", "-no-emul-boot", "-boot-info-table",
		"-eltorito-alt-boot", "-e efi/microsoft/boot/efisys.bin",
		"-o /out/image.iso", "-graft-points",
		"/=/media", "/=/overlay",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n got: %s", want, joined)
		}
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
	// Mixed-case dirs, as a real extracted Windows tree carries.
	mustWrite(t, filepath.Join(root, "BOOT", "etfsboot.com"), "x")
	mustWrite(t, filepath.Join(root, "EFI", "microsoft", "boot", "efisys.bin"), "x")

	bios, uefi := FindBootImages(root)
	if bios != "boot/etfsboot.com" {
		t.Errorf("bios = %q, want boot/etfsboot.com", bios)
	}
	if uefi != "efi/microsoft/boot/efisys.bin" {
		t.Errorf("uefi = %q, want efi/microsoft/boot/efisys.bin", uefi)
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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

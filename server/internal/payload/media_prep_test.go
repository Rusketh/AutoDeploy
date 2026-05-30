package payload

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// withStubSplit replaces the real wimlib splitter with one that writes
// fake .swm parts, so tests don't need a wimlib binary or a real WIM.
func withStubSplit(t *testing.T, parts int) {
	t.Helper()
	orig := splitWIM
	splitWIM = func(_ context.Context, src, firstPart string, _ int) error {
		dir := filepath.Dir(firstPart)
		for i := 1; i <= parts; i++ {
			name := "install.swm"
			if i > 1 {
				name = "install" + strconv.Itoa(i) + ".swm"
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte("swm"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { splitWIM = orig })
}

// mediaTree builds a fake extracted ISO tree with a bootloader and an
// install.wim of the given size (sparse, so size can exceed disk).
func mediaTree(t *testing.T, installName string, size int64, withBootloader bool) string {
	t.Helper()
	root := t.TempDir()
	if withBootloader {
		bd := filepath.Join(root, "efi", "boot")
		if err := os.MkdirAll(bd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bd, "bootx64.efi"), []byte("efi"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "sources")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(src, installName))
	if err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := f.Truncate(size); err != nil { // sparse
			t.Fatal(err)
		}
	}
	f.Close()
	return root
}

func TestPrepareBootMediaSplitsOversized(t *testing.T) {
	withStubSplit(t, 2)
	root := mediaTree(t, "install.wim", swmSplitThresholdBytes+1, true)

	mp := PrepareBootMedia(context.Background(), root)
	if mp.Err != "" {
		t.Fatalf("unexpected prep error: %s", mp.Err)
	}
	if mp.Format != "swm" || mp.SWMParts != 2 {
		t.Errorf("format=%q parts=%d, want swm/2", mp.Format, mp.SWMParts)
	}
	if mp.InstallRel != "sources/install.swm" {
		t.Errorf("InstallRel=%q", mp.InstallRel)
	}
	if !mp.BootloaderPresent {
		t.Errorf("bootloader should be present")
	}
	// Original oversized wim must be gone.
	if _, err := os.Stat(filepath.Join(root, "sources", "install.wim")); !os.IsNotExist(err) {
		t.Errorf("original install.wim should be removed after split")
	}
}

func TestPrepareBootMediaSmallNoSplit(t *testing.T) {
	root := mediaTree(t, "install.wim", 1024, true)
	mp := PrepareBootMedia(context.Background(), root)
	if mp.Err != "" {
		t.Fatalf("unexpected error: %s", mp.Err)
	}
	if mp.SWMParts != 0 || mp.Format != "wim" {
		t.Errorf("parts=%d format=%q, want 0/wim", mp.SWMParts, mp.Format)
	}
	if mp.InstallRel != "sources/install.wim" {
		t.Errorf("InstallRel=%q", mp.InstallRel)
	}
	// Untouched.
	if _, err := os.Stat(filepath.Join(root, "sources", "install.wim")); err != nil {
		t.Errorf("small install.wim should remain: %v", err)
	}
}

func TestPrepareBootMediaIdempotent(t *testing.T) {
	withStubSplit(t, 3)
	root := mediaTree(t, "install.wim", swmSplitThresholdBytes+1, true)

	first := PrepareBootMedia(context.Background(), root)
	if first.SWMParts != 3 {
		t.Fatalf("first run parts=%d", first.SWMParts)
	}
	// Re-running must detect the existing .swm set, not re-split or error.
	splitWIM = func(context.Context, string, string, int) error {
		t.Fatal("splitWIM must not be called on an already-split tree")
		return nil
	}
	second := PrepareBootMedia(context.Background(), root)
	if second.Format != "swm" || second.SWMParts != 3 || second.Err != "" {
		t.Errorf("idempotent run = %+v", second)
	}
}

func TestPrepareBootMediaMissingBootloaderFlagged(t *testing.T) {
	root := mediaTree(t, "install.wim", 1024, false) // no efi/boot/bootx64.efi
	mp := PrepareBootMedia(context.Background(), root)
	if mp.BootloaderPresent {
		t.Errorf("bootloader should be reported absent")
	}
	// Not having a bootloader isn't a prep Err, but DeployReady (model)
	// gates on it; here we just assert the flag.
}

// TestPrepareBootMediaUppercaseBootloader proves the case-insensitive
// path walk: real Windows media is often authored as EFI/BOOT/BOOTX64.EFI,
// which an exact-case check would miss on a case-sensitive filesystem.
func TestPrepareBootMediaUppercaseBootloader(t *testing.T) {
	root := t.TempDir()
	bd := filepath.Join(root, "EFI", "BOOT")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bd, "BOOTX64.EFI"), []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "SOURCES"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SOURCES", "INSTALL.WIM"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mp := PrepareBootMedia(context.Background(), root)
	if !mp.BootloaderPresent {
		t.Errorf("uppercase EFI/BOOT/BOOTX64.EFI should be detected")
	}
}

func TestPrepareBootMediaNoInstallImage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	mp := PrepareBootMedia(context.Background(), root)
	if mp.Err == "" || !strings.Contains(mp.Err, "no install") {
		t.Errorf("expected missing-install-image error, got %q", mp.Err)
	}
}

// TestPrepareBootMediaDropsStaleOriginalAlongsideSwm reproduces the rig
// bug: a re-prepare re-extracted the oversized install.wim next to the
// .swm parts from a prior split, and the idempotency path returned early
// without dropping it -- so the Boot Client tried to copy the >4 GiB
// original onto FAT32 and got ENOSPC.
func TestPrepareBootMediaDropsStaleOriginalAlongsideSwm(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "sources")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	// Existing split parts from a prior prepare.
	for _, n := range []string{"install.swm", "install2.swm"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte("swm"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// And a re-extracted oversized original sitting right beside them.
	f, err := os.Create(filepath.Join(src, "install.wim"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(swmSplitThresholdBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	mp := PrepareBootMedia(context.Background(), root)
	if mp.Format != "swm" || mp.SWMParts != 2 {
		t.Errorf("format=%q parts=%d, want swm/2", mp.Format, mp.SWMParts)
	}
	if _, err := os.Stat(filepath.Join(src, "install.wim")); !os.IsNotExist(err) {
		t.Errorf("oversized install.wim must be removed when .swm parts already exist")
	}
}

// TestExtractISOStartsClean verifies ExtractISO wipes the destination
// first, so a stale file from a previous prepare can't survive into the
// new media tree.
func TestExtractISOStartsClean(t *testing.T) {
	dest := t.TempDir()
	stale := filepath.Join(dest, "stale-install.wim")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcISO := buildTinyISO(t, "sources/install.wim", "wim")
	if _, _, err := ExtractISO(srcISO, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should be wiped before re-extract")
	}
}

func TestPrepareBootMediaSplitFailureRecorded(t *testing.T) {
	orig := splitWIM
	splitWIM = func(context.Context, string, string, int) error {
		return context.DeadlineExceeded
	}
	t.Cleanup(func() { splitWIM = orig })
	root := mediaTree(t, "install.wim", swmSplitThresholdBytes+1, true)

	mp := PrepareBootMedia(context.Background(), root)
	if mp.Err == "" {
		t.Errorf("split failure should be recorded in Err")
	}
	// Original must remain (we don't remove until split succeeds).
	if _, err := os.Stat(filepath.Join(root, "sources", "install.wim")); err != nil {
		t.Errorf("original must survive a failed split: %v", err)
	}
}

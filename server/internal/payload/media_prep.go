package payload

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Boot-the-media deploy prep. A deploy stages a bootable copy of the
// install media on a single FAT32 partition, so an oversized
// sources/install.wim (or .esd) -- larger than FAT32's 4 GiB per-file
// limit -- is split into <4 GiB install.swm parts here, at ISO ingest.
// Windows Setup reads .swm parts natively, so no NTFS/GRUB/shim is
// needed and the Boot Client stays generic. The operator configures
// nothing; this runs automatically after extraction.

const (
	// swmSplitThresholdBytes: install images at or above this are split.
	// 4000 MiB leaves margin under FAT32's 4 GiB-1 ceiling for filesystem
	// overhead and slightly-uneven part sizing.
	swmSplitThresholdBytes int64 = 4000 * 1024 * 1024
	// swmChunkMiB: target size of each .swm part, comfortably under 4 GiB.
	swmChunkMiB = 3800
)

// splitWIM splits src into <chunkMiB parts named after firstPart
// (install.swm, install2.swm, ...). It's a package var so tests can
// stub it without a real wimlib binary.
var splitWIM = func(ctx context.Context, src, firstPart string, chunkMiB int) error {
	cmd := exec.CommandContext(ctx, "wimlib-imagex", "split", src, firstPart, strconv.Itoa(chunkMiB))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wimlib-imagex split: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// MediaPrep is the descriptive outcome of preparing an extracted ISO as
// FAT32 boot media. It never carries a Go error -- problems are reported
// in Err so the row still records what was found and the portal can show
// "needs attention".
type MediaPrep struct {
	Format            string // "wim" | "esd" | "swm" | "" (no install image)
	InstallRel        string // media-relative install image path, slash-separated
	Bytes             int64  // size of the install image before any split
	SWMParts          int    // 0 = not split (fit FAT32); N = split into N
	BootloaderPresent bool   // efi/boot/bootx64.efi present (UEFI-bootable)
	Err               string // non-empty => media not deploy-ready
}

// PrepareBootMedia inspects the extracted media tree under filesDir and,
// if the install image exceeds the FAT32 limit, splits it into .swm
// parts (removing the oversized original). It is idempotent: an
// already-split tree is detected and left alone. filesDir is the
// absolute path to the iso/{id}/files directory.
func PrepareBootMedia(ctx context.Context, filesDir string) MediaPrep {
	var mp MediaPrep
	mp.BootloaderPresent = hasPathInsensitive(filesDir, "efi", "boot", "bootx64.efi")

	sourcesDir := caseDir(filesDir, "sources")
	if sourcesDir == "" {
		mp.Err = "no sources/ directory in media"
		return mp
	}

	// Already split? (idempotency)
	if first, n := findSWM(sourcesDir); n > 0 {
		mp.Format = "swm"
		mp.SWMParts = n
		mp.InstallRel = "sources/" + first
		if fi, err := os.Stat(filepath.Join(sourcesDir, first)); err == nil {
			mp.Bytes = fi.Size()
		}
		// Defence: if an oversized original is sitting next to the .swm
		// parts (e.g. a re-extract re-created it), drop it -- it must not
		// ship in the FAT32 boot media, where a >4 GiB file can't be
		// written.
		if _, abs := findOne(sourcesDir, "install.wim", "install.esd"); abs != "" {
			_ = os.Remove(abs)
		}
		return mp
	}

	// Locate the install image.
	name, abs := findOne(sourcesDir, "install.wim", "install.esd")
	if abs == "" {
		mp.Err = "no install.wim or install.esd in sources/"
		return mp
	}
	fi, err := os.Stat(abs)
	if err != nil {
		mp.Err = "stat install image: " + err.Error()
		return mp
	}
	mp.Bytes = fi.Size()
	mp.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".") // wim|esd

	if fi.Size() < swmSplitThresholdBytes {
		// Fits FAT32 as-is; no split needed.
		mp.InstallRel = "sources/" + name
		return mp
	}

	// Split into .swm parts, then remove the oversized original so Setup
	// sees only the .swm set.
	firstPart := filepath.Join(sourcesDir, "install.swm")
	if err := splitWIM(ctx, abs, firstPart, swmChunkMiB); err != nil {
		mp.Err = err.Error()
		mp.InstallRel = "sources/" + name // unchanged; not deploy-ready
		return mp
	}
	if err := os.Remove(abs); err != nil {
		mp.Err = "remove original after split: " + err.Error()
		return mp
	}
	first, n := findSWM(sourcesDir)
	if n == 0 {
		mp.Err = "split produced no .swm parts"
		return mp
	}
	mp.Format = "swm"
	mp.SWMParts = n
	mp.InstallRel = "sources/" + first
	return mp
}

// findOne returns the actual filename and absolute path of the first of
// names present in dir (case-insensitive), or "","" if none.
func findOne(dir string, names ...string) (string, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	for _, e := range entries {
		if !e.IsDir() && want[strings.ToLower(e.Name())] {
			return e.Name(), filepath.Join(dir, e.Name())
		}
	}
	return "", ""
}

// findSWM returns the lexically-first *.swm filename in dir and the total
// count (case-insensitive).
func findSWM(dir string) (string, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	var first string
	var n int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".swm") {
			continue
		}
		n++
		if first == "" || strings.ToLower(e.Name()) < strings.ToLower(first) {
			first = e.Name()
		}
	}
	return first, n
}

// caseDir returns the absolute path of a child directory of parent
// matching name case-insensitively, or "" if absent.
func caseDir(parent, name string) string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(parent, e.Name())
		}
	}
	return ""
}

// hasPathInsensitive reports whether the path formed by joining parts
// under root exists, matching each component case-insensitively. Windows
// install media is frequently authored with uppercase directory names
// (EFI/BOOT/BOOTX64.EFI), and the ISO extractor preserves the on-disc
// casing, so an exact-case check would miss the bootloader on a
// case-sensitive filesystem.
func hasPathInsensitive(root string, parts ...string) bool {
	cur := root
	for _, want := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return false
		}
		next := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), want) {
				next = filepath.Join(cur, e.Name())
				break
			}
		}
		if next == "" {
			return false
		}
		cur = next
	}
	return true
}

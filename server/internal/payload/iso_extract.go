// Package payload owns upload, extraction and HTTP delivery of the binary
// blobs that AutoDeploy serves to clients: ISO contents (WIM/ESD and
// supporting files), driver packages and software installers, plus the
// generated unattend file (Phase 5).
//
// Reading a Windows install ISO is a real piece of work. AutoDeploy stores
// the uploaded ISO once, then extracts its files so the media can be served
// individually over HTTP. Modern Windows ISOs are UDF (install.wim exceeds
// ISO9660's 4 GiB limit), so extraction prefers an external UDF-capable
// tool (7z/bsdtar) and falls back to the pure-Go iso9660 reader only for
// simple/legacy ISO9660 images when no tool is installed.
package payload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kdomanski/iso9660"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ExtractAndRecord runs the ISO extraction for the given ISO id against the
// canonical on-disk layout (data/iso/{id}/source.iso -> data/iso/{id}/files/)
// and, when a WIM/ESD is found, updates the ISO row's storage_path to point
// inside the extracted tree so the resolver hands it back as an iso-wim
// payload. It is the single place both upload paths (portal multipart + API
// raw PUT) call so an uploaded ISO is immediately deployable without a
// separate manual "Extract" step.
//
// After extraction it runs PrepareBootMedia over the tree (splitting an
// oversized install.wim into .swm parts for FAT32 boot media) and records
// the resulting prep status on the ISO row. storage_path is set to the
// prepared install image (sources/install.swm after a split, else the
// original) so it always points at something that exists in the tree.
//
// Returns the MediaPrep outcome. A non-nil error means extraction itself
// failed; the caller decides whether that is fatal (it should not be for
// an upload — the blob is stored and the operator can re-run Extract).
// Prep-level problems (oversized split failure, missing bootloader) are
// not errors here: they are recorded in MediaPrep.Err / the ISO row so the
// portal can flag "needs attention".
func ExtractAndRecord(ctx context.Context, blobs *storage.BlobStore, isos *model.ISORepo, id model.ID) (MediaPrep, error) {
	srcAbs, destAbs, err := prepPaths(blobs, id)
	if err != nil {
		return MediaPrep{}, err
	}
	if _, _, err := ExtractISO(srcAbs, destAbs); err != nil {
		return MediaPrep{}, err
	}
	return applyPrep(ctx, isos, id, destAbs)
}

// prepPaths resolves the canonical source.iso and files/ paths for an ISO,
// erroring if the source has not been uploaded.
func prepPaths(blobs *storage.BlobStore, id model.ID) (srcAbs, destAbs string, err error) {
	srcRel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "source.iso"))
	srcAbs, err = blobs.Resolve(srcRel)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(srcAbs); errors.Is(statErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("iso %d not uploaded", int64(id))
	}
	destAbs, err = blobs.EnsureDir(filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files")))
	if err != nil {
		return "", "", err
	}
	return srcAbs, destAbs, nil
}

// applyPrep runs PrepareBootMedia over the extracted tree at destAbs,
// makes a missing-tool failure actionable, writes the result onto the ISO
// row and returns it. Shared by the synchronous ExtractAndRecord and the
// async StartPrepareAsync.
func applyPrep(ctx context.Context, isos *model.ISORepo, id model.ID, destAbs string) (MediaPrep, error) {
	iso, err := isos.Get(ctx, id)
	if err != nil {
		return MediaPrep{}, err
	}
	prep := PrepareBootMedia(ctx, destAbs)
	// A "no sources/" or "no install image" failure with no UDF-capable
	// extractor installed is almost certainly a Windows UDF ISO the pure-Go
	// fallback couldn't read. Make that actionable rather than cryptic.
	if prep.Err != "" && !HaveExternalExtractor() &&
		(strings.Contains(prep.Err, "no sources") || strings.Contains(prep.Err, "no install")) {
		prep.Err += " — install p7zip-full (or libarchive-tools) on the server: Windows ISOs are UDF and need 7z/bsdtar to extract"
	}
	// The oversized-WIM split needs wimlib-imagex on the server. Make a
	// missing-binary failure actionable.
	if strings.Contains(prep.Err, "wimlib-imagex") && strings.Contains(prep.Err, "not found") {
		prep.Err += " — install wimtools (Debian/Ubuntu) or wimlib-utils (Fedora/RHEL) on the server"
	}
	now := time.Now().UTC()
	iso.InstallImageFormat = prep.Format
	iso.InstallImageBytes = prep.Bytes
	iso.SWMParts = prep.SWMParts
	iso.BootloaderPresent = prep.BootloaderPresent
	iso.PrepError = prep.Err
	iso.MediaPreparedAt = &now
	if prep.InstallRel != "" {
		iso.StoragePath = filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files", prep.InstallRel))
	}
	// Media summary so the portal can show the whole tree was ingested,
	// not just the install image.
	iso.MediaFileCount, iso.MediaTotalBytes = treeStats(destAbs)
	iso.BootWimPresent = hasPathInsensitive(destAbs, "sources", "boot.wim")
	if err := isos.Update(ctx, iso); err != nil {
		return prep, err
	}
	return prep, nil
}

// errNoExtractor means no external UDF-capable extractor was found on the
// host, so ExtractISO fell back to the pure-Go ISO9660 reader.
var errNoExtractor = errors.New("no external ISO extractor (7z/bsdtar) found")

// extractorBins are the binary names extractWithTool probes, in order.
var extractorBins = []string{"7z", "7zz", "7za", "bsdtar"}

// HaveExternalExtractor reports whether a UDF-capable extractor is
// installed. Used to turn a "no sources/ in media" prep failure into an
// actionable "install p7zip" message, since that failure on a Windows ISO
// almost always means the UDF contents were unreadable without 7z.
func HaveExternalExtractor() bool {
	for _, b := range extractorBins {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

// ExtractISO unpacks the ISO at srcPath into destDir, preserving the
// directory layout, and returns the total bytes written.
//
// Modern Windows 10/11 ISOs are UDF images (because sources/install.wim
// exceeds ISO9660's 4 GiB per-file limit), and the pure-Go ISO9660 reader
// can read neither UDF nor >4 GiB files -- on a Windows ISO it would see
// only the near-empty ISO9660 bridge and miss sources/ and efi/ entirely.
// So we prefer an external UDF-capable extractor (7z, then bsdtar) and
// fall back to the pure-Go reader only for simple/legacy ISO9660 images
// (and the unit tests' tiny synthetic ISOs) when no tool is installed.
//
// wimRelPath is left empty here; the caller (ExtractAndRecord) locates the
// install image via PrepareBootMedia, which walks the extracted tree.
func ExtractISO(srcPath, destDir string) (totalBytes int64, wimRelPath string, err error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, "", err
	}
	switch err := extractWithTool(srcPath, destDir); {
	case err == nil:
		return treeBytes(destDir), "", nil
	case errors.Is(err, errNoExtractor):
		// Fall back to the pure-Go ISO9660 reader (no UDF, <4 GiB).
		return extractISO9660Native(srcPath, destDir)
	default:
		return 0, "", err
	}
}

// extractWithTool extracts srcPath into destDir using the first available
// UDF-capable extractor. Returns errNoExtractor if none is installed.
func extractWithTool(srcPath, destDir string) error {
	type extractor struct {
		bin  string
		args []string
	}
	// 7z (any of its binary names) extracts ISO9660+Joliet+UDF and >4 GiB
	// files, preserving the tree under -o<dest>. bsdtar (libarchive) is a
	// solid second choice.
	candidates := []extractor{
		{"7z", []string{"x", "-y", "-bd", "-o" + destDir, srcPath}},
		{"7zz", []string{"x", "-y", "-bd", "-o" + destDir, srcPath}},
		{"7za", []string{"x", "-y", "-bd", "-o" + destDir, srcPath}},
		{"bsdtar", []string{"-x", "-f", srcPath, "-C", destDir}},
	}
	for _, c := range candidates {
		bin, lookErr := exec.LookPath(c.bin)
		if lookErr != nil {
			continue
		}
		out, runErr := exec.Command(bin, c.args...).CombinedOutput()
		if runErr != nil {
			return fmt.Errorf("%s extract failed: %w: %s", c.bin, runErr, strings.TrimSpace(string(out)))
		}
		// 7z dumps El Torito boot images into a top-level "[BOOT]" dir
		// that is not part of the real media; drop it so it doesn't bloat
		// the boot partition.
		_ = os.RemoveAll(filepath.Join(destDir, "[BOOT]"))
		return nil
	}
	return errNoExtractor
}

// treeBytes sums the size of every regular file under root.
func treeBytes(root string) int64 {
	_, b := treeStats(root)
	return b
}

// treeStats counts the regular files under root and sums their size.
func treeStats(root string) (count int, total int64) {
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		count++
		if info, e := d.Info(); e == nil {
			total += info.Size()
		}
		return nil
	})
	return count, total
}

// extractISO9660Native is the pure-Go fallback: it walks the ISO at
// srcPath and writes every file into destDir. Used only for simple/legacy
// ISO9660 images and the unit tests when no external extractor is present.
// It cannot read UDF or files larger than 4 GiB.
func extractISO9660Native(srcPath, destDir string) (totalBytes int64, wimRelPath string, err error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	img, err := iso9660.OpenImage(f)
	if err != nil {
		return 0, "", fmt.Errorf("open iso: %w", err)
	}
	root, err := img.RootDir()
	if err != nil {
		return 0, "", fmt.Errorf("read iso root: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, "", err
	}

	written, wim, err := walk(root, destDir, "")
	if err != nil {
		return written, wim, err
	}
	return written, wim, nil
}

func walk(node *iso9660.File, destRoot, relPath string) (int64, string, error) {
	var total int64
	var wim string

	if node.IsDir() {
		children, err := node.GetChildren()
		if err != nil {
			return 0, "", err
		}
		for _, c := range children {
			name := c.Name()
			if name == "." || name == ".." {
				continue
			}
			childRel := filepath.Join(relPath, sanitiseISOName(name))
			if c.IsDir() {
				if err := os.MkdirAll(filepath.Join(destRoot, childRel), 0o755); err != nil {
					return total, wim, err
				}
				n, w, err := walk(c, destRoot, childRel)
				total += n
				if w != "" && wim == "" {
					wim = w
				}
				if err != nil {
					return total, wim, err
				}
				continue
			}
			n, w, err := writeFile(c, destRoot, childRel)
			total += n
			if w != "" && wim == "" {
				wim = w
			}
			if err != nil {
				return total, wim, err
			}
		}
		return total, wim, nil
	}
	return 0, "", errors.New("walk called on non-directory node")
}

func writeFile(node *iso9660.File, destRoot, relPath string) (int64, string, error) {
	abs := filepath.Join(destRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return 0, "", err
	}
	out, err := os.Create(abs)
	if err != nil {
		return 0, "", err
	}
	r := node.Reader()
	n, copyErr := io.Copy(out, r)
	closeErr := out.Close()
	if copyErr != nil {
		return n, "", copyErr
	}
	if closeErr != nil {
		return n, "", closeErr
	}

	low := strings.ToLower(filepath.Base(relPath))
	if low == "install.wim" || low == "install.esd" {
		return n, relPath, nil
	}
	return n, "", nil
}

// sanitiseISOName drops the ";1" file-version suffix some ISO9660
// implementations append.
func sanitiseISOName(name string) string {
	if i := strings.Index(name, ";"); i >= 0 {
		return name[:i]
	}
	return name
}

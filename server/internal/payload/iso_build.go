// ISO authoring for USB re-imaging exports. AutoDeploy's normal deploy stages
// a copy of the Windows media onto a ~7 GiB FAT32 partition on the TARGET's
// own disk, which fails on machines whose disk is too small to hold both the
// staged media and a Windows install. Exporting an image as a bootable ISO
// moves the media onto a USB stick (written with Rufus): the internal disk is
// then free for Windows and the small-disk failure disappears.
//
// This file is the low-level authoring step: given an already-prepared Windows
// media tree on disk plus a set of overlay trees (the generated
// autounattend.xml, the injected agent/$OEM$ scripts and the boot-critical
// $WinPEDriver$ drivers), it drives `xorriso` to produce one hybrid BIOS+UEFI
// bootable ISO. The high-level orchestration that assembles those overlays
// from an image resolution lives in iso_export.go.
//
// The El Torito boot catalog is best-effort: Rufus (and most USB-writers) lay
// the ISO's files onto the stick and boot Windows via the fallback
// efi\boot\bootx64.efi path, so the USB boots even when the source tree lacks
// the original boot images. When the boot images ARE present we add the
// catalog so the ISO also boots directly (e.g. in a VM or via Ventoy).
//
// All destructive work funnels through Runner.Exec so the argv can be asserted
// in tests (via a recorder) without xorriso installed.
package payload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// isoBuilderBin is the ISO authoring tool. xorriso is the only widely-packaged
// tool that writes a hybrid ISO with a Windows-style dual (BIOS+UEFI) El Torito
// catalog on Linux; there is no pure-Go equivalent (the iso9660 writer we
// depend on cannot emit a boot catalog).
const isoBuilderBin = "xorriso"

// ISOBuildRunner is the boundary between the authoring code and the host. Real
// builds use OSISORunner; tests use a recorder that captures the argv.
type ISOBuildRunner interface {
	Exec(ctx context.Context, name string, args ...string) error
}

// OSISORunner runs commands via os/exec, streaming output to the log.
type OSISORunner struct {
	Log *slog.Logger
}

// Exec implements ISOBuildRunner.
func (r *OSISORunner) Exec(ctx context.Context, name string, args ...string) error {
	if r.Log != nil {
		r.Log.Info("isobuild.exec",
			slog.String("actor", "server"),
			slog.String("target", name),
			slog.String("args", fmt.Sprintf("%q", args)))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = isoStdoutWriter{r.Log}
	cmd.Stderr = isoStderrWriter{r.Log}
	return cmd.Run()
}

type isoStdoutWriter struct{ log *slog.Logger }

func (w isoStdoutWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Info("isobuild.exec.stdout", slog.String("line", string(p)))
	}
	return len(p), nil
}

type isoStderrWriter struct{ log *slog.Logger }

func (w isoStderrWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		// xorriso is chatty on stderr even on success, so this is Info not Warn.
		w.log.Info("isobuild.exec.stderr", slog.String("line", string(p)))
	}
	return len(p), nil
}

// ISOBuilderAvailable reports whether the ISO authoring tool is installed.
// Callers use this to fail an export early with an actionable message rather
// than deep inside a build.
func ISOBuilderAvailable() bool {
	_, err := exec.LookPath(isoBuilderBin)
	return err == nil
}

// ISOBuilderMissingHint is the actionable install message shown when xorriso is
// not on PATH, mirroring the p7zip/wimlib hints in iso_extract.go.
const ISOBuilderMissingHint = "xorriso not found on the server — install xorriso (Debian/Ubuntu: apt install xorriso; Fedora/RHEL: dnf install xorriso) to export bootable ISOs"

// ISOSpec describes one ISO to author.
type ISOSpec struct {
	// OutPath is the ISO file to create.
	OutPath string
	// VolumeLabel is the ISO9660 volume id. Windows Setup does not require a
	// specific label, but a stable one keeps the media recognisable.
	VolumeLabel string
	// Trees are absolute directories grafted at the ISO root, in order. Later
	// trees override earlier ones on a path conflict, so pass the base media
	// tree first and the overlay (generated autounattend, $OEM$ scripts,
	// $WinPEDriver$ drivers) last.
	Trees []string
	// BIOSBootImg / UEFIBootImg are ISO-root-relative paths (forward slash) to
	// the El Torito boot images WITHIN the merged tree, or "" to omit that
	// catalog entry. Missing images are not fatal — see the package comment.
	BIOSBootImg string
	UEFIBootImg string
}

// biosBootCandidates / uefiBootCandidates are the media-tree-relative locations
// an El Torito boot image is found at, in preference order. A Windows ISO
// extracted with 7z scatters these: the original efi\microsoft\boot\efisys.bin
// survives as a regular file, while 7z also drops raw El Torito images under a
// top-level "[BOOT]" directory. We probe all known spots.
var biosBootCandidates = []string{
	"boot/etfsboot.com",
	"[BOOT]/1-Boot-NoEmul.img",
	"[BOOT]/Boot-NoEmul.img",
}

var uefiBootCandidates = []string{
	"efi/microsoft/boot/efisys.bin",
	"efi/microsoft/boot/efisys_noprompt.bin",
	"[BOOT]/2-Boot-NoEmul.img",
}

// FindBootImages probes mediaDir for BIOS and UEFI El Torito boot images and
// returns their media-relative (forward-slash) paths, or "" for one that is
// absent. Case-insensitive, since extracted Windows trees vary in case.
func FindBootImages(mediaDir string) (bios, uefi string) {
	return firstExisting(mediaDir, biosBootCandidates), firstExisting(mediaDir, uefiBootCandidates)
}

// firstExisting returns the first candidate (relative, forward-slash) that
// resolves to a regular file under root, matched case-insensitively.
func firstExisting(root string, candidates []string) string {
	for _, c := range candidates {
		if abs, ok := resolveInsensitive(root, c); ok {
			if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
				return c
			}
		}
	}
	return ""
}

// resolveInsensitive walks relSlash (a forward-slash relative path) under root,
// matching each component case-insensitively, and returns the real on-disk
// absolute path plus ok. Windows media preserves its authored casing, which
// varies by tree, so an exact-case join would miss files on a case-sensitive
// filesystem.
func resolveInsensitive(root, relSlash string) (string, bool) {
	cur := root
	for _, want := range strings.Split(relSlash, "/") {
		if want == "" {
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		next := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), want) {
				next = filepath.Join(cur, e.Name())
				break
			}
		}
		if next == "" {
			return "", false
		}
		cur = next
	}
	return cur, true
}

// buildXorrisoArgs assembles the `xorriso -as mkisofs` argv for spec. Kept a
// pure function so tests can assert the command shape without xorriso present.
//
// Layout: -graft-points with one "/=<tree>" per tree merges the trees at the
// ISO root (later wins on conflict). `-iso-level 3` and `-udf` allow the >4 GiB
// install.wim / long paths a Windows media tree carries. The El Torito entries
// are added only for boot images that were found.
func buildXorrisoArgs(spec ISOSpec) []string {
	args := []string{
		"-as", "mkisofs",
		"-iso-level", "3",
		"-udf",
		"-V", spec.VolumeLabel,
	}
	if spec.BIOSBootImg != "" {
		args = append(args,
			"-b", spec.BIOSBootImg,
			"-no-emul-boot",
			"-boot-load-size", "8",
			"-boot-info-table",
		)
	}
	if spec.UEFIBootImg != "" {
		// -eltorito-alt-boot separates this from any preceding BIOS entry; when
		// there is no BIOS entry it simply declares the (only) UEFI entry.
		args = append(args,
			"-eltorito-alt-boot",
			"-e", spec.UEFIBootImg,
			"-no-emul-boot",
		)
	}
	args = append(args, "-o", spec.OutPath, "-graft-points")
	for _, t := range spec.Trees {
		args = append(args, "/="+t)
	}
	return args
}

// AuthorISO builds the ISO described by spec via runner. It validates that
// every tree exists and that an output path was given, then runs the builder.
func AuthorISO(ctx context.Context, spec ISOSpec, runner ISOBuildRunner) error {
	if spec.OutPath == "" {
		return fmt.Errorf("iso build: no output path")
	}
	if len(spec.Trees) == 0 {
		return fmt.Errorf("iso build: no source trees")
	}
	for _, t := range spec.Trees {
		if fi, err := os.Stat(t); err != nil || !fi.IsDir() {
			return fmt.Errorf("iso build: source tree %q is not a directory", t)
		}
	}
	if err := os.MkdirAll(filepath.Dir(spec.OutPath), 0o755); err != nil {
		return fmt.Errorf("iso build: prepare output dir: %w", err)
	}
	if err := runner.Exec(ctx, isoBuilderBin, buildXorrisoArgs(spec)...); err != nil {
		return fmt.Errorf("iso build: %s: %w", isoBuilderBin, err)
	}
	return nil
}

// High-level orchestration for exporting an image as a bootable USB/ISO.
//
// Given an image id and the reachable server base URL, ExportImageISO resolves
// the image, generates the overlay files that turn generic Windows media into
// an AutoDeploy USB install — a whole-disk, random-name autounattend.xml, the
// injected agent + SetupComplete.cmd bootstrap, and the boot-critical
// $WinPEDriver$ drivers — stages them next to the prepared media tree, and
// drives AuthorISO (xorriso) to produce one bootable ISO.
//
// Naming is deliberately NOT baked per-machine: the ISO is generic (identical
// for every machine), so Windows installs with a random name and the injected
// agent enrolls by SMBIOS identity on first boot. The server then reconciles
// the machine's name to its binding — see api/agent_handlers.go
// resolveDesiredName, which the agent applies by renaming itself. Where no
// binding/asset exists the machine simply keeps its random name until an
// operator renames it.
package payload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/unattend"
)

// ISOExportDeps are the repositories and helpers ExportImageISO needs. Drivers
// and Runner may be nil (no driver injection / a default OS runner).
type ISOExportDeps struct {
	Resolver *resolve.Resolver
	Blobs    *storage.BlobStore
	Drivers  *model.DriverPackageRepo
	Runner   ISOBuildRunner
}

// ISOExportOptions parameterise a single export.
type ISOExportOptions struct {
	ImageID model.ID
	// BaseURL is the reachable AutoDeploy base URL (scheme://host) baked into
	// the agent bootstrap so the installed machine enrols against this server.
	BaseURL string
	// OutPath is the ISO file to write.
	OutPath string
	// AgentBinaryPath is the absolute path to the Windows agent .exe to inject.
	// Empty produces an ISO that images Windows but leaves the machine
	// unmanaged (no enrolment, so no name reconciliation) — callers normally
	// resolve this from the downloads directory.
	AgentBinaryPath string
	// WorkDir is a scratch directory for staging the overlay tree. The caller
	// owns its lifecycle (create + remove); a per-export temp dir is typical.
	WorkDir string
	// VolumeLabel overrides the ISO volume id (default "AUTODEPLOY").
	VolumeLabel string
	// Progress, if set, is called with a coarse stage label and percent.
	Progress func(stage string, pct int)
	// Warn, if set, receives non-fatal problems worth showing the operator
	// (e.g. the ISO will only boot UEFI machines).
	Warn func(msg string)
}

// ExportPreflightError is a problem with the image itself (no ISO, media not
// prepared, an edition the ISO does not contain) rather than a server fault.
// Its message is written for the operator; the API returns it as a 4xx so the
// portal can show it before any background work starts.
type ExportPreflightError struct{ Msg string }

func (e *ExportPreflightError) Error() string { return e.Msg }

// PreflightExport resolves the image and checks everything an export needs
// that can be known up front — an ISO, prepared boot media, and an answer-file
// edition the ISO actually contains — so the request fails immediately with a
// clear message instead of minutes later (or, for the edition, on the target
// machine, where Setup stops with "no images are available"). Problems with the
// image are returned as *ExportPreflightError.
func (d ISOExportDeps) PreflightExport(ctx context.Context, imageID model.ID) (resolve.Resolved, error) {
	if d.Resolver == nil || d.Blobs == nil {
		return resolve.Resolved{}, fmt.Errorf("iso export: resolver and blob store are required")
	}
	res, err := d.Resolver.Resolve(ctx, imageID)
	if err != nil {
		return resolve.Resolved{}, fmt.Errorf("iso export: resolve image %d: %w", int64(imageID), err)
	}
	if res.ISO == nil {
		return res, &ExportPreflightError{"This image (and its parents) link no ISO, so there is no Windows media to export. Link an ISO to the image first."}
	}
	if !res.ISO.DeployReady() {
		reason := res.ISO.PrepError
		if reason == "" {
			reason = "boot media not prepared"
		}
		return res, &ExportPreflightError{fmt.Sprintf("ISO %q is not ready to deploy (%s). Prepare it under ISOs first.", res.ISO.Name, reason)}
	}
	s, err := exportSettings(res)
	if err != nil {
		return res, &ExportPreflightError{"The image's unattend settings could not be read: " + err.Error()}
	}
	if err := checkExportEdition(res.ISO, s); err != nil {
		return res, err
	}
	return res, nil
}

// checkExportEdition verifies the answer file's /IMAGE/NAME (the unattend's
// Edition, or unattend.DefaultEdition when unset) is one of the editions the
// ISO's install image carries. ISOs prepared by an older server have no
// recorded editions; those are not checked (nothing to compare against).
func checkExportEdition(iso *model.ISO, s unattend.Settings) error {
	if iso == nil || len(iso.Editions) == 0 {
		return nil
	}
	want := strings.TrimSpace(s.InstallEdition())
	for _, e := range iso.Editions {
		if strings.EqualFold(strings.TrimSpace(e), want) {
			return nil
		}
	}
	how := "Set the Edition in this image's unattend to one of these"
	if s.Edition == "" {
		how = "The unattend leaves Edition unset, so it defaults to " + unattend.DefaultEdition + ". Set it to one of these"
	}
	return &ExportPreflightError{fmt.Sprintf(
		"The answer file would install edition %q, but ISO %q only contains: %s. %s, then export again.",
		want, iso.Name, strings.Join(iso.Editions, ", "), how)}
}

// ExportImageISO builds the bootable ISO described by opts. It returns an error
// on any hard failure (image not resolvable, media not ready, xorriso failure);
// best-effort steps (a driver package that won't stage) are skipped with no
// error so one bad package never blocks an export.
func (d ISOExportDeps) ExportImageISO(ctx context.Context, opts ISOExportOptions) error {
	progress := opts.Progress
	if progress == nil {
		progress = func(string, int) {}
	}
	warn := opts.Warn
	if warn == nil {
		warn = func(string) {}
	}
	if d.Resolver == nil || d.Blobs == nil {
		return fmt.Errorf("iso export: resolver and blob store are required")
	}
	if !ISOBuilderAvailable() {
		return fmt.Errorf("iso export: %s", ISOBuilderMissingHint)
	}
	base, ok := safeServerBase(opts.BaseURL)
	if opts.AgentBinaryPath != "" && !ok {
		return fmt.Errorf("iso export: server base URL %q is not a safe scheme://host", opts.BaseURL)
	}

	progress("Resolving image", 5)
	res, err := d.PreflightExport(ctx, opts.ImageID)
	if err != nil {
		return err
	}

	// The extracted media tree the boot media was prepared from.
	mediaDir, err := d.Blobs.Resolve(filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(res.ISO.ID)), "files")))
	if err != nil {
		return fmt.Errorf("iso export: resolve media dir: %w", err)
	}
	if fi, statErr := os.Stat(mediaDir); statErr != nil || !fi.IsDir() {
		return fmt.Errorf("iso export: media tree %q missing — re-prepare the ISO", mediaDir)
	}

	// A media tree with no El Torito image at all would author an ISO that
	// boots nowhere (and that Rufus rejects as non-bootable): refuse rather
	// than hand the operator a dud after minutes of work. One missing image is
	// survivable but worth saying.
	bios, uefi := FindBootImages(mediaDir)
	switch {
	case bios == "" && uefi == "":
		return fmt.Errorf("iso export: the media of ISO %q has no boot images (boot/etfsboot.com or efi/microsoft/boot/efisys*.bin), so the exported ISO would not boot — re-upload the original Windows ISO", res.ISO.Name)
	case uefi == "":
		warn("The media has no UEFI boot image (efi/microsoft/boot/efisys.bin): the ISO boots legacy-BIOS machines only when started directly. A Rufus-written USB still boots UEFI through efi/boot/bootx64.efi.")
	case bios == "":
		warn("The media has no BIOS boot image (boot/etfsboot.com): the exported ISO and USB boot UEFI machines only.")
	}

	overlayDir := filepath.Join(opts.WorkDir, "overlay")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return fmt.Errorf("iso export: prepare overlay dir: %w", err)
	}
	// Every overlay path is spelled in the media's own case (see
	// mediaCaseRel): ISO9660/Joliet/Rock Ridge names are case-sensitive in the
	// image, so an overlay "sources" grafted over a media "SOURCES" would
	// become a SECOND directory (and a second answer file) instead of merging.
	inMedia := func(rel string) string { return filepath.Join(overlayDir, mediaCaseRel(mediaDir, rel)) }

	// 1. autounattend.xml — guarded whole-disk install, random name, callbacks
	//    suppressed. Written under the media's own spelling so it REPLACES any
	//    answer file the source ISO shipped (e.g. "AutoUnattend.xml").
	progress("Generating answer file", 20)
	xml, err := d.generateExportUnattend(ctx, res)
	if err != nil {
		return fmt.Errorf("iso export: generate unattend: %w", err)
	}
	if err := writeOverlayFile(inMedia("autounattend.xml"), xml); err != nil {
		return fmt.Errorf("iso export: write autounattend.xml: %w", err)
	}
	// The disk guard the answer file's windowsPE pass runs (unattend/diskguard.go).
	if err := writeOverlayFile(inMedia(unattend.DiskGuardScriptPath), unattend.DiskGuardScript(exportDiskID(res))); err != nil {
		return fmt.Errorf("iso export: write disk guard: %w", err)
	}

	// 2. Agent bootstrap into the $OEM$ tree (only when an agent is available).
	if opts.AgentBinaryPath != "" {
		progress("Injecting management agent", 35)
		if err := stageExportAgent(inMedia, opts.AgentBinaryPath, base, opts.ImageID); err != nil {
			return fmt.Errorf("iso export: stage agent: %w", err)
		}
	}

	// 3. Boot-critical drivers into $WinPEDriver$ (best-effort).
	progress("Injecting boot drivers", 50)
	d.stageExportDrivers(ctx, inMedia("$WinPEDriver$"))

	// 4. Author the ISO.
	progress("Authoring ISO", 65)
	label := opts.VolumeLabel
	if label == "" {
		label = "AUTODEPLOY"
	}
	runner := d.Runner
	if runner == nil {
		runner = &OSISORunner{}
	}
	spec := ISOSpec{
		OutPath:     opts.OutPath,
		VolumeLabel: label,
		Trees:       []string{mediaDir, overlayDir},
		BIOSBootImg: bios,
		UEFIBootImg: uefi,
	}
	if err := AuthorISO(ctx, spec, runner); err != nil {
		return err
	}
	progress("Done", 100)
	return nil
}

// generateExportUnattend produces the generic answer file baked into an export:
// the image's own unattend settings, but switched to the whole-disk clean-
// install layout with a random computer name, no per-machine identity, and no
// legacy domain-join block (the agent joins AD after first boot, once the
// machine has been renamed). Install-status callbacks are suppressed because
// the exported media cannot know a per-machine SMBIOS UUID at build time.
//
// The disk layout is the GUARDED whole-disk install (unattend DiskGuard): the
// media now lives on a USB stick that WinPE may number as disk 0, so the static
// "wipe DiskID 0" DiskConfiguration is replaced by a WinPE script that only
// partitions the target after proving it is not the stick — see
// unattend/diskguard.go.
func (d ISOExportDeps) generateExportUnattend(ctx context.Context, res resolve.Resolved) ([]byte, error) {
	s, err := exportSettings(res)
	if err != nil {
		return nil, err
	}
	_ = ctx
	return unattend.Generate(s)
}

// exportSettings is the image's unattend settings rewritten for a generic USB/
// ISO install (see generateExportUnattend).
func exportSettings(res resolve.Resolved) (unattend.Settings, error) {
	var s unattend.Settings
	if res.Unattend != nil {
		parsed, err := unattend.Parse(res.Unattend.SettingsJSON)
		if err != nil {
			return unattend.Settings{}, err
		}
		s = parsed
	} else {
		s = unattend.Defaults()
	}
	s.WholeDisk = true  // clean install onto the whole (small) disk
	s.DiskGuard = true  // ...but only once WinPE proves it is not the USB stick
	s.NameTemplate = "" // random Windows name; the agent renames post-boot
	s.NameIdentity = unattend.NameIdentity{}
	s.DomainJoin = nil  // agent-driven join happens after the rename
	s.ServerURL = ""    // suppress the boot-time deploy-status callbacks
	s.CallbackUUID = "" // (no per-machine UUID is known at build time)
	return s, nil
}

// exportDiskID is the disk the export installs to (the unattend's DiskID,
// default 0) — the one the disk guard script checks before partitioning.
func exportDiskID(res resolve.Resolved) int {
	s, err := exportSettings(res)
	if err != nil {
		return 0
	}
	return s.DiskID
}

// mediaCaseRel re-spells the forward-slash relative path rel using the casing
// the media tree already has for each component that exists there (matched
// case-insensitively), keeping rel's own spelling for components that don't.
// The result is an OS-separator relative path for the overlay tree. E.g. with
// media "SOURCES/" present, "sources/$OEM$/$$" becomes "SOURCES/$OEM$/$$", so
// xorriso merges the overlay INTO the media's directory rather than creating a
// sibling "sources" the installer may or may not pick.
func mediaCaseRel(mediaDir, rel string) string {
	cur := mediaDir
	exists := true
	var out []string
	for _, want := range strings.Split(rel, "/") {
		if want == "" {
			continue
		}
		name := want
		if exists {
			exists = false
			if entries, err := os.ReadDir(cur); err == nil {
				for _, e := range entries {
					if strings.EqualFold(e.Name(), want) {
						name, exists = e.Name(), true
						break
					}
				}
			}
			cur = filepath.Join(cur, name)
		}
		out = append(out, name)
	}
	return filepath.Join(out...)
}

// writeOverlayFile writes data to path, creating parent directories.
func writeOverlayFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// stageExportAgent injects the agent binary and a SetupComplete.cmd bootstrap
// into the overlay's $OEM$ tree, mirroring the boot client's $OEM$ layout so
// Windows Setup copies them into the installed OS:
//
//	sources\$OEM$\$$\AutoDeploy\autodeploy-agent.exe -> C:\Windows\AutoDeploy\...
//	sources\$OEM$\$$\Setup\Scripts\SetupComplete.cmd -> C:\Windows\Setup\Scripts\...
//
// at maps a media-relative path to its overlay location (in the media's case).
// SetupComplete.cmd runs once as SYSTEM at the end of Setup; it enrols the
// machine against baseURL (self-identifying by SMBIOS UUID), records the image,
// and installs the resident service.
func stageExportAgent(at func(rel string) string, agentSrc, baseURL string, imageID model.ID) error {
	if err := copyFile(agentSrc, at("sources/$OEM$/$$/AutoDeploy/autodeploy-agent.exe")); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	script := exportSetupCompleteScript(baseURL, imageID)
	return writeOverlayFile(at("sources/$OEM$/$$/Setup/Scripts/SetupComplete.cmd"), []byte(script))
}

// exportSetupCompleteScript renders the SetupComplete.cmd for an exported ISO.
// Unlike the boot client's variant (which bakes a server-minted agent_id), this
// one enrols on first run: `--server <base> --image-id <id>` self-identifies by
// SMBIOS UUID, mints and persists an agent_id, records the image, then exits;
// `install-service` makes the agent resident. baseURL is pre-validated by
// safeServerBase so it carries no cmd metacharacters.
func exportSetupCompleteScript(baseURL string, imageID model.ID) string {
	// CRLF line endings — this is a Windows batch file.
	return strings.Join([]string{
		"@echo off",
		"rem Generated by AutoDeploy USB/ISO export. Runs once as SYSTEM at end of Setup.",
		`set "SRC=%WINDIR%\AutoDeploy\autodeploy-agent.exe"`,
		`set "DEST=%ProgramFiles%\AutoDeploy"`,
		`if not exist "%DEST%" mkdir "%DEST%"`,
		`copy /y "%SRC%" "%DEST%\autodeploy-agent.exe" >nul`,
		fmt.Sprintf(`reg add "HKLM\SOFTWARE\AutoDeploy" /v ServerURL /t REG_SZ /d "%s" /f`, baseURL),
		fmt.Sprintf(`"%%DEST%%\autodeploy-agent.exe" --server "%s" --image-id %d`, baseURL, int64(imageID)),
		`"%DEST%\autodeploy-agent.exe" install-service`,
		"exit /b 0",
		"",
	}, "\r\n")
}

// stageExportDrivers copies the boot-critical (storage/NIC) subtrees of every
// driver package into the overlay's $WinPEDriver$ tree so WinPE can reach the
// target disk on whatever hardware the generic USB is used against. The rest of
// each package is NOT baked in — the resident agent installs matched drivers
// online after first boot. Entirely best-effort: a package without an extracted
// tree, or with no boot-critical INF, is silently skipped. driverRoot is the
// overlay's $WinPEDriver$ directory.
func (d ISOExportDeps) stageExportDrivers(ctx context.Context, driverRoot string) {
	if d.Drivers == nil {
		return
	}
	pkgs, err := d.Drivers.List(ctx)
	if err != nil {
		return
	}
	for _, p := range pkgs {
		meta, ok, err := ReadDriverMetadata(d.Blobs, p.ID)
		if err != nil || !ok {
			continue // not extracted — no reliable boot-critical split
		}
		dirs := bootCriticalDirs(meta)
		if len(dirs) == 0 {
			continue // nothing WinPE needs from this package
		}
		srcRoot, err := d.Blobs.Resolve(filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(p.ID)), "files")))
		if err != nil {
			continue
		}
		name := sanitizeDirName(p.Name, fmt.Sprint(int64(p.ID)))
		for _, sub := range dirs {
			rel := strings.Trim(strings.ReplaceAll(sub, `\`, "/"), "/")
			var src, dst string
			if rel == "" || rel == "." {
				src = srcRoot
				dst = filepath.Join(driverRoot, name)
			} else {
				relOS := filepath.FromSlash(rel)
				src = filepath.Join(srcRoot, relOS)
				dst = filepath.Join(driverRoot, name, relOS)
			}
			_ = copyTree(src, dst) // best-effort
		}
	}
}

// sanitizeDirName reduces a package name to a filesystem/ISO-safe directory
// name, falling back to fallback when nothing usable remains.
func sanitizeDirName(name, fallback string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.':
			b.WriteRune(c)
		case c == ' ':
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "driver-" + fallback
	}
	return out
}

// safeServerBase validates raw as an http(s) URL whose authority is a bare
// host[:port] with no path/query/userinfo, returning the canonical
// "scheme://host" for safe embedding in a batch file. Mirrors the boot client's
// sanitizeCmdValue character set (alphanumerics plus / : - . _), so the value
// can never carry cmd metacharacters.
func safeServerBase(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	scheme := ""
	rest := ""
	switch {
	case strings.HasPrefix(raw, "https://"):
		scheme, rest = "https", strings.TrimPrefix(raw, "https://")
	case strings.HasPrefix(raw, "http://"):
		scheme, rest = "http", strings.TrimPrefix(raw, "http://")
	default:
		return "", false
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.ContainsAny(rest, "/?#@ ") {
		return "", false
	}
	for _, c := range rest {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.' || c == '-' || c == ':' || c == '_':
		default:
			return "", false
		}
	}
	return scheme + "://" + rest, true
}

// copyFile copies a single regular file, creating parent directories.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree recursively copies the directory (or file) at src to dst.
func copyTree(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return copyFile(src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

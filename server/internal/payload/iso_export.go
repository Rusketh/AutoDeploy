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
	res, err := d.Resolver.Resolve(ctx, opts.ImageID)
	if err != nil {
		return fmt.Errorf("iso export: resolve image %d: %w", int64(opts.ImageID), err)
	}
	if res.ISO == nil {
		return fmt.Errorf("iso export: image %d resolves to no ISO", int64(opts.ImageID))
	}
	if !res.ISO.DeployReady() {
		reason := res.ISO.PrepError
		if reason == "" {
			reason = "boot media not prepared"
		}
		return fmt.Errorf("iso export: ISO %q is not deploy-ready: %s", res.ISO.Name, reason)
	}

	// The extracted media tree the boot media was prepared from.
	mediaDir, err := d.Blobs.Resolve(filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(res.ISO.ID)), "files")))
	if err != nil {
		return fmt.Errorf("iso export: resolve media dir: %w", err)
	}
	if fi, statErr := os.Stat(mediaDir); statErr != nil || !fi.IsDir() {
		return fmt.Errorf("iso export: media tree %q missing — re-prepare the ISO", mediaDir)
	}

	overlayDir := filepath.Join(opts.WorkDir, "overlay")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return fmt.Errorf("iso export: prepare overlay dir: %w", err)
	}

	// 1. autounattend.xml — whole-disk, random name, callbacks suppressed.
	progress("Generating answer file", 20)
	xml, err := d.generateExportUnattend(ctx, res)
	if err != nil {
		return fmt.Errorf("iso export: generate unattend: %w", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "autounattend.xml"), xml, 0o644); err != nil {
		return fmt.Errorf("iso export: write autounattend.xml: %w", err)
	}

	// 2. Agent bootstrap into the $OEM$ tree (only when an agent is available).
	if opts.AgentBinaryPath != "" {
		progress("Injecting management agent", 35)
		if err := stageExportAgent(overlayDir, opts.AgentBinaryPath, base, opts.ImageID); err != nil {
			return fmt.Errorf("iso export: stage agent: %w", err)
		}
	}

	// 3. Boot-critical drivers into $WinPEDriver$ (best-effort).
	progress("Injecting boot drivers", 50)
	d.stageExportDrivers(ctx, overlayDir)

	// 4. Author the ISO.
	progress("Authoring ISO", 65)
	bios, uefi := FindBootImages(mediaDir)
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
func (d ISOExportDeps) generateExportUnattend(ctx context.Context, res resolve.Resolved) ([]byte, error) {
	var s unattend.Settings
	if res.Unattend != nil {
		parsed, err := unattend.Parse(res.Unattend.SettingsJSON)
		if err != nil {
			return nil, err
		}
		s = parsed
	} else {
		s = unattend.Defaults()
	}
	s.WholeDisk = true  // clean install onto the whole (small) disk
	s.NameTemplate = "" // random Windows name; the agent renames post-boot
	s.NameIdentity = unattend.NameIdentity{}
	s.DomainJoin = nil  // agent-driven join happens after the rename
	s.ServerURL = ""    // suppress the boot-time deploy-status callbacks
	s.CallbackUUID = "" // (no per-machine UUID is known at build time)
	_ = ctx
	return unattend.Generate(s)
}

// stageExportAgent injects the agent binary and a SetupComplete.cmd bootstrap
// into the overlay's $OEM$ tree, mirroring the boot client's $OEM$ layout so
// Windows Setup copies them into the installed OS:
//
//	sources\$OEM$\$$\AutoDeploy\autodeploy-agent.exe -> C:\Windows\AutoDeploy\...
//	sources\$OEM$\$$\Setup\Scripts\SetupComplete.cmd -> C:\Windows\Setup\Scripts\...
//
// SetupComplete.cmd runs once as SYSTEM at the end of Setup; it enrols the
// machine against baseURL (self-identifying by SMBIOS UUID), records the image,
// and installs the resident service.
func stageExportAgent(overlayDir, agentSrc, baseURL string, imageID model.ID) error {
	oemWin := filepath.Join(overlayDir, "sources", "$OEM$", "$$")
	agentDir := filepath.Join(oemWin, "AutoDeploy")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	if err := copyFile(agentSrc, filepath.Join(agentDir, "autodeploy-agent.exe")); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	scriptsDir := filepath.Join(oemWin, "Setup", "Scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return err
	}
	script := exportSetupCompleteScript(baseURL, imageID)
	return os.WriteFile(filepath.Join(scriptsDir, "SetupComplete.cmd"), []byte(script), 0o644)
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
// tree, or with no boot-critical INF, is silently skipped.
func (d ISOExportDeps) stageExportDrivers(ctx context.Context, overlayDir string) {
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
				dst = filepath.Join(overlayDir, "$WinPEDriver$", name)
			} else {
				relOS := filepath.FromSlash(rel)
				src = filepath.Join(srcRoot, relOS)
				dst = filepath.Join(overlayDir, "$WinPEDriver$", name, relOS)
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

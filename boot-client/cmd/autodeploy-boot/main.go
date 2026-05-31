// Command autodeploy-boot is the AutoDeploy Boot Client. It runs in the
// pre-OS environment chainloaded over HTTP by iPXE.
//
// FAIL-SAFE: every failure path that cannot continue with imaging exits 0
// without touching the disk. The firmware then boots the existing OS. The
// Boot Client never authorises, resolves or decides — those happen on the
// server. This binary reports facts and executes instructions.
//
// Sub-commands:
//
//	identify          Print SMBIOS identity and exit.
//	menu              Report identity, fetch deployment menu, render and
//	                  wait for an operator selection (interactive).
//	deploy <image-id> Fetch the manifest for the image, mirror the install
//	                  media, stage it onto a bootable FAT32 partition and
//	                  reboot into the media's own installer.
//
// Flags affecting all sub-commands:
//
//	-server <url>     AutoDeploy server base URL (required for menu/deploy).
//	-sysfs <path>     DMI sysfs root (override for testing).
//	-insecure-tls     Skip TLS cert verification (dev only).
//	-disk <device>    Target disk device for deploy (default /dev/sda).
//	-work <dir>       Scratch directory (default /run/autodeploy).
//	-dry-run          Log destructive steps without executing them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/boot-client/internal/httpc"
	"github.com/rusketh/autodeploy/boot-client/internal/imaging"
	"github.com/rusketh/autodeploy/boot-client/internal/logging"
	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
)

type bootFlags struct {
	server      string
	sysfs       string
	insecureTLS bool
	disk        string
	work        string
	dryRun      bool
	site        string
}

// Version is set at build time via -ldflags
// "-X main.Version=v0.1.2". The Boot Client logs it on every run so
// the centralised log search can attribute events to a specific
// release.
var Version = "dev"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "-v" {
			os.Stdout.WriteString(Version + "\n")
			return
		}
	}
	var f bootFlags
	flag.StringVar(&f.server, "server", "", "AutoDeploy server base URL")
	flag.StringVar(&f.sysfs, "sysfs", "/sys/class/dmi/id", "DMI sysfs root")
	flag.BoolVar(&f.insecureTLS, "insecure-tls", false, "Skip TLS verification (dev only)")
	flag.StringVar(&f.disk, "disk", "/dev/sda", "Target disk device")
	flag.StringVar(&f.work, "work", "/run/autodeploy", "Scratch directory")
	flag.BoolVar(&f.dryRun, "dry-run", false, "Log destructive steps without executing them")
	flag.StringVar(&f.site, "site", "", "Site name forwarded to the server so payload downloads route to a site-local mirror")
	flag.Parse()

	// Also accept the site via kernel command line — DHCP option 175 or
	// the iPXE chainload script can set autodeploy.site=<name>.
	if f.site == "" {
		f.site = siteFromKernelCmdline()
	}

	log, shipper := logging.NewWithShipper(os.Stdout, "boot", 2048)
	// Best-effort log shipment to the server. Drained from the run's
	// buffer right before we exit (or just before reboot) so the
	// portal can see what happened on this client during deploy.
	defer shipLogs(log, shipper, f.server, f.insecureTLS)

	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "menu"
	}

	id, err := smbios.ReadFromSysfs(f.sysfs)
	if err != nil {
		log.Error("smbios.read", slog.String("error", err.Error()))
		os.Exit(0) // fail-safe: no SMBIOS → normal boot
	}
	log.Info("boot.start",
		slog.String("actor", id.SystemUUID),
		slog.String("target", "self"),
		slog.String("manufacturer", id.SystemManufacturer),
		slog.String("product", id.SystemProduct),
		slog.String("serial", id.SystemSerial),
		slog.String("cmd", cmd),
		slog.String("version", Version),
	)

	switch cmd {
	case "identify":
		// SMBIOS already logged; nothing else to do.
		return
	case "menu":
		if f.server == "" {
			log.Info("boot.idle", slog.String("reason", "no server configured; fail-safe to normal boot"))
			return
		}
		runMenu(log, f, id, shipper)
	case "deploy":
		if f.server == "" {
			log.Error("deploy", slog.String("error", "server URL required"))
			os.Exit(0)
		}
		imageID, err := strconv.ParseInt(flag.Arg(1), 10, 64)
		if err != nil || imageID <= 0 {
			log.Error("deploy", slog.String("error", "deploy <image-id> required"))
			os.Exit(0)
		}
		runDeploy(log, f, id, imageID, shipper)
	default:
		log.Error("boot.unknown_cmd", slog.String("cmd", cmd))
		os.Exit(0)
	}
}

// MenuItem mirrors the server's BootMenuItem to keep this binary
// self-contained without a shared schema package.
type MenuItem struct {
	ImageID     int64  `json:"image_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type menuResponse struct {
	Items   []MenuItem `json:"items"`
	Reimage *MenuItem  `json:"reimage,omitempty"`
}

type manifestItem struct {
	Role string `json:"role"`
	URL  string `json:"url"`
	Base string `json:"base,omitempty"`       // iso-media: prefix for index paths
	Size int64  `json:"size_bytes,omitempty"` // iso-media: total media size
	Name string `json:"name,omitempty"`
}

// mediaIndex mirrors the server's /payload/iso/{id}/index.json: every file
// in the extracted media tree the Boot Client must mirror.
type mediaIndex struct {
	Files []struct {
		Path string `json:"path"`
		Size int64  `json:"size_bytes"`
	} `json:"files"`
}

type manifest struct {
	ImageID  int64          `json:"image_id"`
	AgentID  string         `json:"agent_id,omitempty"`
	Items    []manifestItem `json:"items"`
	Warnings []string       `json:"warnings,omitempty"`
}

func runMenu(log *slog.Logger, f bootFlags, id smbios.Identity, shipper *logging.Shipper) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := httpc.New(f.server, id.SystemUUID, f.insecureTLS).WithSite(f.site)

	// Access PIN gate. Three attempts; on the third failure or lock-out,
	// fail-safe to a normal boot. The Boot Client never decides whether
	// a PIN is correct — every attempt is server-validated.
	if !runAccessPIN(ctx, log, c, id) {
		return
	}

	var resp menuResponse
	if err := c.PostJSON(ctx, "/api/v1/clients/menu", map[string]any{
		"system_uuid":         id.SystemUUID,
		"system_manufacturer": id.SystemManufacturer,
		"system_product":      id.SystemProduct,
		"system_serial":       id.SystemSerial,
	}, &resp); err != nil {
		log.Error("menu.fetch", slog.String("error", err.Error()))
		os.Exit(0) // fail-safe
	}
	if len(resp.Items) == 0 && resp.Reimage == nil {
		log.Info("menu.empty", slog.String("reason", "no deployable images; fail-safe to normal boot"))
		return
	}
	// Fetch branding so the menu shows the operator's product /
	// organisation name. Failure is silent: the menu still renders
	// with the AutoDeploy default.
	brand := fetchBrand(ctx, c, log)
	fmt.Println()
	fmt.Printf("=== %s ===\n", brandTitle(brand))
	if resp.Reimage != nil {
		fmt.Printf("  R) Re-image this machine: %s\n", resp.Reimage.Name)
	}
	for i, it := range resp.Items {
		fmt.Printf("  %d) %s — %s\n", i+1, it.Name, it.Description)
	}
	fmt.Println("  0) Cancel and boot normally")
	fmt.Print("\nSelect: ")
	var choice string
	if _, err := fmt.Scanln(&choice); err != nil {
		log.Info("menu.cancel", slog.String("reason", "no input; fail-safe"))
		return
	}
	if choice == "0" || choice == "" {
		log.Info("menu.cancel", slog.String("reason", "operator cancelled"))
		return
	}
	if choice == "R" || choice == "r" {
		if resp.Reimage == nil {
			log.Info("menu.cancel", slog.String("reason", "no re-image option"))
			return
		}
		runDeploy(log, f, id, resp.Reimage.ImageID, shipper)
		return
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(resp.Items) {
		log.Info("menu.cancel", slog.String("reason", "invalid choice"))
		return
	}
	runDeploy(log, f, id, resp.Items[n-1].ImageID, shipper)
}

func runDeploy(log *slog.Logger, f bootFlags, id smbios.Identity, imageID int64, shipper *logging.Shipper) {
	ctx := context.Background()
	c := httpc.New(f.server, id.SystemUUID, f.insecureTLS).WithSite(f.site)
	var m manifest
	// POST identity so the server can match driver packages.
	if err := c.PostJSON(ctx, fmt.Sprintf("/api/v1/images/%d/manifest", imageID),
		identityBody(id), &m); err != nil {
		log.Error("manifest.fetch", slog.String("error", err.Error()))
		os.Exit(0)
	}
	log.Info("manifest.received",
		slog.String("actor", id.SystemUUID),
		slog.String("target", fmt.Sprintf("image:%d", imageID)),
		slog.Int("items", len(m.Items)),
		slog.Any("warnings", m.Warnings),
	)

	if err := os.MkdirAll(f.work, 0o755); err != nil {
		log.Error("workdir.create", slog.String("error", err.Error()))
		os.Exit(0)
	}

	// Download the small payloads (unattend, drivers) into the work dir,
	// but only CAPTURE the iso-media item -- the multi-GB media tree is
	// streamed directly onto the FAT32 boot partition later, never into
	// the RAM-backed work dir (which a full Windows media would exhaust).
	var mediaItem *manifestItem
	var unattendPath string
	var driverPaths []string
	for i := range m.Items {
		it := m.Items[i]
		switch it.Role {
		case "iso-media":
			mediaItem = &m.Items[i]
		case "unattend":
			dst := filepath.Join(f.work, "payload-unattend-"+sanitise(it.URL))
			if err := download(ctx, c, log, it, dst); err != nil {
				log.Error("download", slog.String("url", it.URL), slog.String("error", err.Error()))
				os.Exit(0)
			}
			unattendPath = dst
		case "driver":
			dst := filepath.Join(f.work, "payload-driver-"+sanitise(it.URL))
			if err := download(ctx, c, log, it, dst); err != nil {
				log.Error("download", slog.String("url", it.URL), slog.String("error", err.Error()))
				os.Exit(0)
			}
			driverPaths = append(driverPaths, dst)
		case "software":
			// Software is installed post-OS by the agent, not staged onto
			// the boot media. Skip it here -- no need to pull it pre-OS.
			continue
		}
	}

	if mediaItem == nil {
		log.Error("deploy.no_media", slog.String("reason", "manifest has no iso-media; fail-safe"))
		os.Exit(0)
	}

	// Fetch the agent and render its SetupComplete.cmd. Best-effort: if no
	// agent is served, the deploy still proceeds (just without an agent).
	agentPath := fetchAgent(ctx, c, log, f.work)
	setupCompletePath := ""
	if agentPath != "" {
		if m.AgentID == "" {
			log.Warn("agent.no_id", slog.String("reason", "manifest carried no agent_id; agent will have no identity"))
		}
		if scp, err := writeSetupComplete(f.work, f.server, m.AgentID); err != nil {
			log.Warn("agent.setupcomplete", slog.String("error", err.Error()))
			agentPath = "" // no installer script -> don't strand a copied-but-uninstalled binary
		} else {
			setupCompletePath = scp
		}
	}

	plan := imaging.MediaPlan{
		TargetDisk:        f.disk,
		MediaBytes:        mediaItem.Size,
		UnattendPath:      unattendPath,
		DriverPaths:       driverPaths,
		AgentPath:         agentPath,
		SetupCompletePath: setupCompletePath,
		WorkDir:           f.work,
	}
	runner := &imaging.OSRunner{Log: log, DryRun: f.dryRun}
	log.Info("deploy.stage.start",
		slog.String("actor", id.SystemUUID),
		slog.String("target", f.disk),
		slog.Bool("dry_run", f.dryRun))

	// Partition + format + mount the FAT32 boot partition, then stream the
	// media tree straight onto it (on disk), avoiding a RAM staging copy.
	mountPath, err := imaging.PreparePartition(ctx, plan, runner)
	if err != nil {
		log.Error("deploy.partition.fail", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if !f.dryRun {
		if err := downloadMedia(ctx, c, log, *mediaItem, mountPath); err != nil {
			log.Error("download.media",
				slog.String("url", mediaItem.URL), slog.String("error", err.Error()))
			os.Exit(1) // partition already wiped; do not reboot into a broken disk
		}
	}
	if err := imaging.FinalizeMedia(ctx, plan, runner, mountPath); err != nil {
		log.Error("deploy.stage.fail", slog.String("error", err.Error()))
		os.Exit(1) // do NOT reboot: caller's environment can investigate
	}
	// Best-effort: register the firmware boot entry. The media is fully
	// staged by now, and it carries the \EFI\BOOT\BOOTX64.EFI fallback, so
	// a failure here (e.g. efibootmgr quirks) should warn and still reboot
	// rather than strand a ready-to-boot disk in a shell.
	if err := imaging.RegisterBootEntry(ctx, plan, runner); err != nil {
		log.Warn("deploy.bootentry.fail",
			slog.String("error", err.Error()),
			slog.String("note", `relying on \EFI\BOOT\BOOTX64.EFI fallback; if the machine doesn't boot the staged media, add a UEFI boot entry for it manually`))
	}
	log.Info("deploy.stage.ok",
		slog.String("actor", id.SystemUUID),
		slog.String("target", f.disk))
	if f.dryRun {
		log.Info("deploy.reboot.skip", slog.String("reason", "dry run"))
		return
	}
	// Ship logs BEFORE asking the kernel to reboot -- once reboot is
	// in flight the network goes down and the buffered events are
	// lost. The main()'s deferred shipLogs covers the failure paths
	// above where we exit cleanly without rebooting.
	shipLogs(log, shipper, f.server, f.insecureTLS)
	// Reboot into the staged media. "-f" forces an immediate reboot via the
	// reboot() syscall WITHOUT contacting an init system -- required here
	// because the bundled /sbin/reboot may be systemd's, which otherwise
	// fails in the initramfs with "Failed to talk to init daemon". Works
	// the same for busybox's reboot applet.
	if err := runner.Exec(ctx, "reboot", "-f"); err != nil {
		log.Error("reboot", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// downloadMedia mirrors an iso-media payload: it fetches the media index
// (it.URL) then downloads every listed file under destDir, preserving the
// tree. Each file comes from it.Base + path. Paths are validated to stay
// within destDir (defence against a malformed index).
func downloadMedia(ctx context.Context, c *httpc.Client, log *slog.Logger, it manifestItem, destDir string) error {
	var buf bytes.Buffer
	if err := c.Download(ctx, it.URL, &buf, nil); err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}
	var idx mediaIndex
	if err := json.Unmarshal(buf.Bytes(), &idx); err != nil {
		return fmt.Errorf("parse index: %w", err)
	}
	if len(idx.Files) == 0 {
		return fmt.Errorf("media index lists no files")
	}
	// The boot media is a single FAT32 partition (4 GiB per-file limit).
	// An oversized install.wim should have been split into .swm parts
	// server-side; if one slipped through, fail with a clear diagnostic
	// up front rather than a cryptic ENOSPC partway through the copy.
	const fat32MaxFile = 4*1024*1024*1024 - 1
	for _, mf := range idx.Files {
		if mf.Size > fat32MaxFile {
			return fmt.Errorf("media file %s is %d bytes, over the FAT32 4 GiB limit; "+
				"the server-side split did not run -- re-prepare the ISO", mf.Path, mf.Size)
		}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for i, mf := range idx.Files {
		rel := filepath.FromSlash(mf.Path)
		out := filepath.Join(destDir, rel)
		// Reject any path that escapes destDir.
		if !strings.HasPrefix(out, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("index path escapes media dir: %q", mf.Path)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		derr := c.Download(ctx, it.Base+mf.Path, w, nil)
		_ = w.Close()
		if derr != nil {
			return fmt.Errorf("file %s: %w", mf.Path, derr)
		}
		if (i+1)%200 == 0 {
			log.Info("download.media.progress",
				slog.Int("done", i+1), slog.Int("total", len(idx.Files)))
		}
	}
	log.Info("download.media.ok", slog.Int("files", len(idx.Files)))
	return nil
}

func download(ctx context.Context, c *httpc.Client, log *slog.Logger, it manifestItem, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	return c.Download(ctx, it.URL, out, func(n int64) {
		log.Info("download.progress",
			slog.String("role", it.Role),
			slog.String("url", it.URL),
			slog.Int64("bytes", n))
	})
}

// agentUpdateInfo mirrors the server's /api/v1/agent/update-info response.
type agentUpdateInfo struct {
	URL string `json:"url"`
}

// fetchAgent downloads the Windows agent binary the server serves (if any)
// into work and returns its path. Best-effort: an empty return means no
// agent is available, and the deploy proceeds without one rather than
// failing -- a machine with no agent is still a successfully imaged
// machine.
func fetchAgent(ctx context.Context, c *httpc.Client, log *slog.Logger, work string) string {
	var info agentUpdateInfo
	if err := c.GetJSON(ctx, "/api/v1/agent/update-info", &info); err != nil {
		log.Warn("agent.update_info", slog.String("error", err.Error()))
		return ""
	}
	if info.URL == "" {
		log.Warn("agent.unavailable", slog.String("reason", "server serves no agent binary"))
		return ""
	}
	dst := filepath.Join(work, "payload-agent.exe")
	out, err := os.Create(dst)
	if err != nil {
		log.Warn("agent.create", slog.String("error", err.Error()))
		return ""
	}
	defer out.Close()
	if err := c.Download(ctx, info.URL, out, nil); err != nil {
		log.Warn("agent.download", slog.String("url", info.URL), slog.String("error", err.Error()))
		return ""
	}
	log.Info("agent.fetched", slog.String("url", info.URL))
	return dst
}

// setupCompleteTemplate is the SetupComplete.cmd Windows Setup runs as
// SYSTEM at the end of installation. It copies the agent (staged by the
// $OEM$ tree into C:\Windows\AutoDeploy) into Program Files, provisions the
// server URL + this machine's server-minted agent_id into the registry,
// and registers the agent as a real Windows service. The agent then only
// ever needs the registry -- it identifies itself by agent_id and polls
// /api/v1/agent/self. Server URL and agent_id are baked in at stage time.
const setupCompleteTemplate = "@echo off\r\n" +
	"rem Generated by AutoDeploy. Runs once as SYSTEM at the end of Setup.\r\n" +
	"set \"SRC=%WINDIR%\\AutoDeploy\\autodeploy-agent.exe\"\r\n" +
	"set \"DEST=%ProgramFiles%\\AutoDeploy\"\r\n" +
	"if not exist \"%DEST%\" mkdir \"%DEST%\"\r\n" +
	"copy /y \"%SRC%\" \"%DEST%\\autodeploy-agent.exe\" >nul\r\n" +
	"reg add \"HKLM\\SOFTWARE\\AutoDeploy\" /v ServerURL /t REG_SZ /d \"{{SERVER}}\" /f\r\n" +
	"reg add \"HKLM\\SOFTWARE\\AutoDeploy\" /v AgentID /t REG_SZ /d \"{{AGENTID}}\" /f\r\n" +
	"\"%DEST%\\autodeploy-agent.exe\" install-service\r\n" +
	"exit /b 0\r\n"

// writeSetupComplete renders SetupComplete.cmd with the server URL and the
// machine's agent_id baked in, and writes it to work, returning its path.
func writeSetupComplete(work, serverURL, agentID string) (string, error) {
	content := strings.ReplaceAll(setupCompleteTemplate, "{{SERVER}}", serverURL)
	content = strings.ReplaceAll(content, "{{AGENTID}}", agentID)
	dst := filepath.Join(work, "SetupComplete.cmd")
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// runAccessPIN prompts the operator for the global access PIN (if the
// server has one configured) and submits each attempt to the server for
// validation. Returns true when access is granted (or the server has no
// PIN configured); returns false after three failures or a lock-out, in
// which case the caller should NOT proceed to the menu.
//
// Every failure path here exits 0 / returns false: imaging never proceeds
// on an access denial.
func runAccessPIN(ctx context.Context, log *slog.Logger, c *httpc.Client, id smbios.Identity) bool {
	type pinResp struct {
		Granted   bool `json:"granted"`
		LockedOut bool `json:"locked_out,omitempty"`
	}
	tryOnce := func(pin string) (pinResp, error) {
		var r pinResp
		err := c.PostJSON(ctx, "/api/v1/clients/validate-pin", map[string]any{
			"system_uuid": id.SystemUUID,
			"pin":         pin,
		}, &r)
		return r, err
	}
	// First call with empty PIN: when the server has no gate configured
	// it grants immediately; otherwise it records a failed attempt and
	// we prompt for real.
	r, err := tryOnce("")
	if err != nil {
		log.Error("pin.validate", slog.String("error", err.Error()))
		return false // fail-safe
	}
	if r.Granted {
		return true
	}
	for attempt := 1; attempt <= 3; attempt++ {
		fmt.Printf("\nAutoDeploy access PIN (attempt %d/3): ", attempt)
		var pin string
		if _, err := fmt.Scanln(&pin); err != nil || pin == "" {
			log.Info("pin.cancel", slog.String("reason", "no input"))
			return false
		}
		r, err := tryOnce(pin)
		if err != nil {
			log.Error("pin.validate", slog.String("error", err.Error()))
			return false
		}
		if r.LockedOut {
			log.Warn("pin.locked_out",
				slog.String("actor", id.SystemUUID),
				slog.String("target", "self"))
			fmt.Println("Too many failed attempts. Booting normally.")
			return false
		}
		if r.Granted {
			log.Info("pin.granted", slog.String("actor", id.SystemUUID))
			return true
		}
		log.Warn("pin.failed",
			slog.String("actor", id.SystemUUID),
			slog.Int("attempt", attempt))
	}
	fmt.Println("Three failed PIN attempts. Booting normally.")
	return false
}

// identityBody is the SMBIOS-shaped JSON body the server expects for
// driver-matching resolution.
func identityBody(id smbios.Identity) map[string]any {
	return map[string]any{
		"system_manufacturer": id.SystemManufacturer,
		"system_product":      id.SystemProduct,
		"system_serial":       id.SystemSerial,
		"system_uuid":         id.SystemUUID,
		"system_sku":          id.SystemSKU,
		"system_family":       id.SystemFamily,
		"bios_vendor":         id.BIOSVendor,
		"bios_version":        id.BIOSVersion,
		"board_manufacturer":  id.BoardManufacturer,
		"board_product":       id.BoardProduct,
		"board_serial":        id.BoardSerial,
	}
}

// siteFromKernelCmdline parses /proc/cmdline looking for
// autodeploy.site=<name>. Empty on read failure or absence.
func siteFromKernelCmdline() string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, tok := range bytesFields(b) {
		const k = "autodeploy.site="
		if len(tok) > len(k) && string(tok[:len(k)]) == k {
			return string(tok[len(k):])
		}
	}
	return ""
}

// bytesFields is a tiny strings.Fields for a []byte without an import.
func bytesFields(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if start >= 0 {
				out = append(out, b[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, b[start:])
	}
	return out
}

// brandResp is the subset of /api/v1/branding the boot menu shows.
type brandResp struct {
	ProductName      string `json:"product_name"`
	OrganisationName string `json:"organisation_name"`
}

// fetchBrand reads the operator's branding from the server so the
// boot menu can render org-specific text. The endpoint is open (no
// auth) by design -- the boot screen needs the brand before the
// access PIN gate runs. Failure is silent: defaults take over.
func fetchBrand(ctx context.Context, c *httpc.Client, log *slog.Logger) brandResp {
	var b brandResp
	if err := c.GetJSON(ctx, "/api/v1/branding", &b); err != nil {
		log.Info("brand.fetch.skip", slog.String("reason", err.Error()))
		return brandResp{ProductName: "AutoDeploy"}
	}
	if b.ProductName == "" {
		b.ProductName = "AutoDeploy"
	}
	return b
}

// brandTitle renders the menu header. If the operator has set an
// organisation name we lead with it; otherwise the product name
// alone is enough.
func brandTitle(b brandResp) string {
	if b.OrganisationName != "" {
		return b.OrganisationName + " — " + b.ProductName
	}
	return b.ProductName
}

// shipLogs is the defer hook that flushes the buffered slog records
// to the server. Failures here are themselves logged but cannot then
// be shipped, so they only appear on local stdout. We give the
// network a generous timeout so a flaky link doesn't drop the batch
// silently. Empty server URL skips the ship altogether (e.g. the
// 'identify' subcommand).
func shipLogs(log *slog.Logger, shipper *logging.Shipper, server string, insecureTLS bool) {
	if server == "" || shipper == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := shipper.Ship(ctx, server, insecureTLS)
	if err != nil {
		log.Warn("logs.ship.fail",
			slog.String("error", err.Error()),
			slog.Int("buffered", n),
		)
		return
	}
	log.Info("logs.ship.ok", slog.Int("events", n))
}

// sanitise turns a URL fragment into a filename-safe string.
func sanitise(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 96 {
		out = out[len(out)-96:]
	}
	return string(out)
}

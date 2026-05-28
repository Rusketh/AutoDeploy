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
//   identify          Print SMBIOS identity and exit.
//   menu              Report identity, fetch deployment menu, render and
//                     wait for an operator selection (interactive).
//   deploy <image-id> Fetch the manifest for the image, download payloads,
//                     partition and image the target disk.
//
// Flags affecting all sub-commands:
//
//   -server <url>     AutoDeploy server base URL (required for menu/deploy).
//   -sysfs <path>     DMI sysfs root (override for testing).
//   -insecure-tls     Skip TLS cert verification (dev only).
//   -disk <device>    Target disk device for deploy (default /dev/sda).
//   -work <dir>       Scratch directory (default /run/autodeploy).
//   -dry-run          Log destructive steps without executing them.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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

func main() {
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

	log := logging.New(os.Stdout, "boot")

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
		runMenu(log, f, id)
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
		runDeploy(log, f, id, imageID)
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
	Items    []MenuItem `json:"items"`
	Reimage  *MenuItem  `json:"reimage,omitempty"`
}

type manifestItem struct {
	Role string `json:"role"`
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

type manifest struct {
	ImageID  int64          `json:"image_id"`
	Items    []manifestItem `json:"items"`
	Warnings []string       `json:"warnings,omitempty"`
}

func runMenu(log *slog.Logger, f bootFlags, id smbios.Identity) {
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
	fmt.Println()
	fmt.Println("=== AutoDeploy ===")
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
		runDeploy(log, f, id, resp.Reimage.ImageID)
		return
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(resp.Items) {
		log.Info("menu.cancel", slog.String("reason", "invalid choice"))
		return
	}
	runDeploy(log, f, id, resp.Items[n-1].ImageID)
}

func runDeploy(log *slog.Logger, f bootFlags, id smbios.Identity, imageID int64) {
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

	var wimPath, unattendPath string
	var driverPaths []string
	for _, it := range m.Items {
		dst := filepath.Join(f.work, "payload-"+it.Role+"-"+sanitise(it.URL))
		if err := download(ctx, c, log, it, dst); err != nil {
			log.Error("download", slog.String("url", it.URL), slog.String("error", err.Error()))
			os.Exit(0)
		}
		switch it.Role {
		case "iso-wim":
			wimPath = dst
		case "unattend":
			unattendPath = dst
		case "driver":
			driverPaths = append(driverPaths, dst)
		}
	}

	if wimPath == "" {
		log.Error("deploy.no_wim", slog.String("reason", "manifest has no WIM/ESD; fail-safe"))
		os.Exit(0)
	}

	plan := imaging.Plan{
		TargetDisk:    f.disk,
		WIMPath:       wimPath,
		WIMImageIndex: 1,
		UnattendPath:  unattendPath,
		DriverPaths:   driverPaths,
		WorkDir:       f.work,
	}
	runner := &imaging.OSRunner{Log: log, DryRun: f.dryRun}
	log.Info("deploy.apply.start",
		slog.String("actor", id.SystemUUID),
		slog.String("target", f.disk),
		slog.Bool("dry_run", f.dryRun))
	if err := imaging.Apply(ctx, plan, runner); err != nil {
		log.Error("deploy.apply.fail", slog.String("error", err.Error()))
		os.Exit(1) // do NOT reboot: caller's environment can investigate
	}
	log.Info("deploy.apply.ok",
		slog.String("actor", id.SystemUUID),
		slog.String("target", f.disk))
	if f.dryRun {
		log.Info("deploy.reboot.skip", slog.String("reason", "dry run"))
		return
	}
	// Reboot into the freshly applied OS.
	if err := runner.Exec(ctx, "reboot"); err != nil {
		log.Error("reboot", slog.String("error", err.Error()))
		os.Exit(1)
	}
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

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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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
	// AutoDeployImageID is set when the server has flagged this machine for
	// remote re-image. Non-zero means deploy it immediately, no menu.
	AutoDeployImageID int64 `json:"auto_deploy_image_id,omitempty"`
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
	// Remote re-image: the server flagged this machine to auto-deploy. Skip
	// BOTH the access-PIN gate and the interactive menu -- the operator
	// already authorised this re-image server-side, and there's no human at
	// the console to type a PIN or pick an image. The server clears the
	// flag when runDeploy reports "staging", so this fires exactly once.
	if resp.AutoDeployImageID != 0 {
		log.Info("menu.auto_reimage",
			slog.String("actor", id.SystemUUID),
			slog.Int64("image_id", resp.AutoDeployImageID))
		runDeploy(log, f, id, resp.AutoDeployImageID, shipper)
		return
	}

	if len(resp.Items) == 0 && resp.Reimage == nil {
		log.Info("menu.empty", slog.String("reason", "no deployable images; fail-safe to normal boot"))
		return
	}
	// Fetch branding (product/org name + primary colour) for the UI.
	// Failure is silent: defaults take over.
	brand := fetchBrand(ctx, c, log)

	// Try the graphical UI first; if the framebuffer/input can't be
	// brought up (serial console, odd firmware) startInteractive returns
	// false and we fall back to the text console. Both paths run the same
	// PIN gate and call runDeploy with the chosen image.
	if startInteractive(ctx, log, f, c, id, resp, brand, shipper) {
		return
	}
	runConsoleMenu(ctx, log, f, c, id, resp, brand, shipper)
}

// runConsoleMenu is the text-console fallback: PIN gate, then the numbered
// menu. Unchanged behaviour from the original boot client.
func runConsoleMenu(ctx context.Context, log *slog.Logger, f bootFlags, c *httpc.Client, id smbios.Identity, resp menuResponse, brand brandResp, shipper *logging.Shipper) {
	if !runAccessPIN(ctx, log, c, id) {
		return
	}
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

// reportDeployStatus tells the server a deploy is staging / staged /
// failed, so the dashboard shows a machine before its OS (and agent) come
// up. Best-effort: a reporting failure never blocks or fails the deploy.
func reportDeployStatus(ctx context.Context, c *httpc.Client, log *slog.Logger, id smbios.Identity, imageID int64, status, notes string) {
	body := map[string]any{
		"identity": identityBody(id),
		"image_id": imageID,
		"status":   status,
		"notes":    notes,
	}
	if err := c.PostJSON(ctx, "/api/v1/clients/deploy-status", body, nil); err != nil {
		log.Warn("deploy.status.report", slog.String("status", status), slog.String("error", err.Error()))
	}
}

// Stall budgets for resilient downloads -- how long DownloadFile keeps
// retrying with no forward progress before giving up. Post-wipe media gets
// the longest budget: the disk is already wiped, so we ride out a long outage
// rather than strand a half-written disk. Pre-wipe payloads get less, because
// giving up there just falls back to a safe normal boot; the optional agent
// gets the least.
const (
	mediaStallBudget   = 30 * time.Minute
	payloadStallBudget = 5 * time.Minute
	agentStallBudget   = 60 * time.Second
)

// reacquireNetwork re-runs link-up + DHCP on the wired interfaces. The Boot
// Client doesn't own the network (the initramfs brought it up at boot), but a
// long imaging run can outlast the DHCP lease or ride through a link bounce --
// and then a fresh lease is what lets a resumed download continue. Best-effort,
// mirrors the initramfs init's bring-up, and is a no-op off Linux (the glob
// matches nothing, so udhcpc/ip are never invoked).
func reacquireNetwork(ctx context.Context, log *slog.Logger) {
	ifaces, _ := filepath.Glob("/sys/class/net/*")
	for _, dev := range ifaces {
		ifc := filepath.Base(dev)
		if ifc == "lo" {
			continue
		}
		_ = exec.CommandContext(ctx, "ip", "link", "set", ifc, "up").Run()
	}
	for _, dev := range ifaces {
		ifc := filepath.Base(dev)
		if ifc == "lo" {
			continue
		}
		// One-shot DHCP (-n: give up if no lease); we retry again next round.
		cmd := exec.CommandContext(ctx, "udhcpc", "-i", ifc, "-n", "-q",
			"-t", "5", "-T", "3", "-s", "/usr/share/udhcpc/default.script")
		if err := cmd.Run(); err != nil {
			log.Warn("net.reacquire", slog.String("if", ifc), slog.String("error", err.Error()))
			continue
		}
		log.Info("net.reacquire.ok", slog.String("if", ifc))
	}
	// A USB NIC that reset under load re-enumerates as a FRESH netdev with
	// hardware offloads back on and autosuspend active -- the very state that
	// stalls a large transfer. Re-apply the hardening after every re-DHCP so
	// the resumed download doesn't hit the same wall that interrupted it.
	hardenUSBNICs(ctx, log)
}

// hardenUSBNICs disables the two things that make Realtek RTL8153 / r8152
// (and similar) USB Ethernet adapters stall a large transfer partway through
// -- the exact failure that leaves a deploy looping on "Network interrupted -
// retrying": NIC hardware offloads (TSO/GSO/GRO/checksum), and USB
// autosuspend (the device powers down during a lull in the copy and the link
// never comes back). Small control-plane calls pass; the multi-MB/GB payload
// download hangs. The common Dell USB-C / USB 3.0 PXE adapter is exactly this
// chipset.
//
// The initramfs hardens once at boot and the kernel cmdline carries
// usbcore.autosuspend=-1, but neither survives a mid-deploy re-enumeration:
// the new netdev is born with offloads on again. So runDeploy applies this
// before the downloads and reacquireNetwork re-applies it after each retry.
// Best-effort and Linux-only -- off Linux the sysfs walk matches nothing and
// ethtool simply isn't on PATH.
func hardenUSBNICs(ctx context.Context, log *slog.Logger) {
	hardenUSBNICsAt(ctx, log, "/sys")
}

func hardenUSBNICsAt(ctx context.Context, log *slog.Logger, sysfsRoot string) {
	for _, ifc := range listNetInterfaces(sysfsRoot) {
		dev, isUSB := usbNetDevicePath(sysfsRoot, ifc)
		if !isUSB {
			continue // leave PCIe NICs alone -- they keep offloads for throughput
		}
		// Offloads off (best-effort; needs ethtool, bundled in the initrd).
		_ = exec.CommandContext(ctx, "ethtool", "-K", ifc,
			"tso", "off", "gso", "off", "gro", "off",
			"tx", "off", "rx", "off", "sg", "off").Run()
		// Autosuspend off on the device and the USB chain above it.
		pinned := disableUSBAutosuspend(dev)
		log.Info("net.usbnic.harden",
			slog.String("if", ifc),
			slog.Int("autosuspend_pinned", pinned))
	}
}

// listNetInterfaces returns the non-loopback interface names under
// <sysfsRoot>/class/net. Empty (not an error) when the directory is absent --
// e.g. off Linux, or in a test with no fake tree.
func listNetInterfaces(sysfsRoot string) []string {
	entries, err := os.ReadDir(filepath.Join(sysfsRoot, "class", "net"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Name() == "lo" {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// usbNetDevicePath resolves the backing device directory for interface ifc
// (<sysfsRoot>/class/net/<ifc>/device) and reports whether it sits on the USB
// bus -- i.e. whether the resolved sysfs path has a "usb" component, the same
// test the initramfs uses. Returns ("", false) when the link is absent.
func usbNetDevicePath(sysfsRoot, ifc string) (string, bool) {
	p, err := filepath.EvalSymlinks(filepath.Join(sysfsRoot, "class", "net", ifc, "device"))
	if err != nil {
		return "", false
	}
	return p, strings.Contains(p, "usb")
}

// disableUSBAutosuspend pins a USB device awake by writing "on" to the
// power/control of devPath and each of its USB ancestors (and -1 to
// autosuspend_delay_ms where present), walking up the sysfs tree until the
// path leaves USB. This stops the RTL8153/r8152 autosuspend-mid-transfer
// hang. Returns how many power/control files it flipped (for logging/tests).
func disableUSBAutosuspend(devPath string) int {
	pinned := 0
	for p := devPath; strings.Contains(p, "usb"); {
		if writeSysfs(filepath.Join(p, "power", "control"), "on") {
			pinned++
		}
		writeSysfs(filepath.Join(p, "power", "autosuspend_delay_ms"), "-1")
		parent := filepath.Dir(p)
		if parent == p {
			break // reached the filesystem root -- stop
		}
		p = parent
	}
	return pinned
}

// writeSysfs writes val to a sysfs attribute, reporting success. Best-effort:
// a missing or read-only attribute is not an error worth surfacing.
func writeSysfs(path, val string) bool {
	return os.WriteFile(path, []byte(val), 0o644) == nil
}

func runDeploy(log *slog.Logger, f bootFlags, id smbios.Identity, imageID int64, shipper *logging.Shipper) {
	ctx := context.Background()
	c := httpc.New(f.server, id.SystemUUID, f.insecureTLS).WithSite(f.site)
	// Open a deployment row right away so a machine that fails before its
	// OS ever boots is still visible on the dashboard.
	reportDeployStatus(ctx, c, log, id, imageID, "staging", "")
	reportStage("Preparing", "Fetching deployment manifest", -1)
	var m manifest
	// POST identity so the server can match driver packages.
	if err := c.PostJSON(ctx, fmt.Sprintf("/api/v1/images/%d/manifest", imageID),
		identityBody(id), &m); err != nil {
		log.Error("manifest.fetch", slog.String("error", err.Error()))
		reportDeployStatus(ctx, c, log, id, imageID, "failed", "manifest fetch failed: "+err.Error())
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

	// Harden any USB Ethernet adapter before the multi-MB/GB payload
	// downloads begin: disable hardware offloads and USB autosuspend, the two
	// things that stall a large transfer on Realtek RTL8153 / r8152 dongles
	// (the common Dell USB-C / USB 3.0 PXE adapter). The initramfs does this
	// at boot too; repeating it here covers a device that enumerated late, or
	// a boot image whose build host lacked ethtool.
	hardenUSBNICs(ctx, log)

	reportStage("Preparing", "Fetching drivers, answer file and agent", -1)
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
			reportFile(itemLabel(it))
			dst := filepath.Join(f.work, "payload-unattend-"+sanitise(it.URL))
			if err := download(ctx, c, log, it, dst); err != nil {
				log.Error("download", slog.String("url", it.URL), slog.String("error", err.Error()))
				os.Exit(0)
			}
			unattendPath = dst
		case "driver":
			reportFile(itemLabel(it))
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
		reportDeployStatus(ctx, c, log, id, imageID, "failed", "manifest has no deployable media")
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

	// Stage adcb.ps1 (the install-status milestone reporter) regardless of
	// agent presence, so the dashboard advances through specialize/first-logon
	// even for agent-less deploys. The script is static -- the unattend passes
	// it the server URL, UUID and status as args -- so nothing is baked in
	// here. Best-effort: a write failure just means no Setup-phase milestones.
	callbackScriptPath := ""
	if csp, err := writeCallbackScript(f.work); err != nil {
		log.Warn("callback.script", slog.String("error", err.Error()))
	} else {
		callbackScriptPath = csp
	}

	plan := imaging.MediaPlan{
		TargetDisk:         f.disk,
		MediaBytes:         mediaItem.Size,
		UnattendPath:       unattendPath,
		DriverPaths:        driverPaths,
		AgentPath:          agentPath,
		SetupCompletePath:  setupCompletePath,
		CallbackScriptPath: callbackScriptPath,
		WorkDir:            f.work,
	}
	runner := &imaging.OSRunner{Log: log, DryRun: f.dryRun}
	log.Info("deploy.stage.start",
		slog.String("actor", id.SystemUUID),
		slog.String("target", f.disk),
		slog.Bool("dry_run", f.dryRun))

	// Fetch and validate the media index BEFORE wiping the disk. This
	// ensures the server is reachable and the media payload exists; if
	// the index fetch fails the existing disk is left untouched.
	var mediaIdx *mediaIndex
	if !f.dryRun {
		reportStage("Validating media", "Fetching media index", -1)
		reportFile("media file index")
		idx, err := fetchMediaIndex(ctx, c, *mediaItem)
		if err != nil {
			log.Error("deploy.media_index.fail", slog.String("error", err.Error()))
			reportDeployStatus(ctx, c, log, id, imageID, "failed", "media index fetch failed: "+err.Error())
			os.Exit(0) // fail-safe: disk untouched, normal boot
		}
		mediaIdx = idx
	}

	// Partition + format + mount the FAT32 boot partition, then stream the
	// media tree straight onto it (on disk), avoiding a RAM staging copy.
	reportStage("Preparing disk", "Partitioning and formatting", -1)
	mountPath, err := imaging.PreparePartition(ctx, plan, runner)
	if err != nil {
		log.Error("deploy.partition.fail", slog.String("error", err.Error()))
		reportDeployStatus(ctx, c, log, id, imageID, "failed", "partition failed: "+err.Error())
		os.Exit(1)
	}
	if !f.dryRun {
		reportStage("Downloading image", "Copying install media to disk", 0)
		if err := downloadMediaFiles(ctx, c, log, *mediaItem, mountPath, mediaIdx); err != nil {
			log.Error("download.media",
				slog.String("url", mediaItem.URL), slog.String("error", err.Error()))
			reportDeployStatus(ctx, c, log, id, imageID, "failed", "media download failed: "+err.Error())
			os.Exit(1) // partition already wiped; do not reboot into a broken disk
		}
	}
	reportStage("Finalising", "Writing answer file, drivers and agent", -1)
	reportFile("")
	if err := imaging.FinalizeMedia(ctx, plan, runner, mountPath); err != nil {
		log.Error("deploy.stage.fail", slog.String("error", err.Error()))
		reportDeployStatus(ctx, c, log, id, imageID, "failed", "media staging failed: "+err.Error())
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
	reportDeployStatus(ctx, c, log, id, imageID, "staged", "")
	reportStage("Ready", "Rebooting into Windows Setup", 100)
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

// fetchMediaIndex downloads and validates the media index for an iso-media
// manifest item. It is called BEFORE the disk is wiped so that a server
// error or missing payload leaves the existing disk intact.
func fetchMediaIndex(ctx context.Context, c *httpc.Client, it manifestItem) (*mediaIndex, error) {
	var buf bytes.Buffer
	if err := c.Download(ctx, it.URL, &buf, nil); err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}
	var idx mediaIndex
	if err := json.Unmarshal(buf.Bytes(), &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if len(idx.Files) == 0 {
		return nil, fmt.Errorf("media index lists no files")
	}
	// The boot media is a single FAT32 partition (4 GiB per-file limit).
	// An oversized install.wim should have been split into .swm parts
	// server-side; if one slipped through, fail with a clear diagnostic
	// up front rather than a cryptic ENOSPC partway through the copy.
	const fat32MaxFile = 4*1024*1024*1024 - 1
	for _, mf := range idx.Files {
		if mf.Size > fat32MaxFile {
			return nil, fmt.Errorf("media file %s is %d bytes, over the FAT32 4 GiB limit; "+
				"the server-side split did not run -- re-prepare the ISO", mf.Path, mf.Size)
		}
	}
	return &idx, nil
}

// downloadMediaFiles mirrors an iso-media payload onto destDir using a
// previously fetched index. Each file comes from it.Base + path. Paths are
// validated to stay within destDir (defence against a malformed index).
func downloadMediaFiles(ctx context.Context, c *httpc.Client, log *slog.Logger, it manifestItem, destDir string, idx *mediaIndex) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	// Sum the tree up front so progress is byte-weighted: one multi-GB
	// install.wim then advances the bar smoothly instead of the bar jumping
	// per file, and -- the point here -- a stalled file shows as a frozen bar
	// on a named file rather than an ambiguous spinner.
	var total int64
	for _, mf := range idx.Files {
		total += mf.Size
	}
	var done int64
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
		// Show the media-relative path being copied (never a URL) so the
		// operator sees the current file and exactly which one a stall is on.
		reportFile(mf.Path)
		base := done
		// Resilient copy: resume from disk via HTTP Range and ride out a
		// mid-copy network outage by retrying with backoff, instead of
		// aborting onto a half-written, unbootable disk. When the network
		// returns it picks up exactly where it left off.
		err := c.DownloadFile(ctx, it.Base+mf.Path, out, mf.Size, mediaStallBudget,
			func(have int64) {
				if total > 0 {
					reportStage("Downloading image", "Copying install media to disk",
						int((base+have)*100/total))
				}
			},
			func(attempt int, derr error) {
				log.Warn("download.media.retry",
					slog.String("file", mf.Path),
					slog.Int("attempt", attempt),
					slog.String("error", derr.Error()))
				pct := -1
				if total > 0 {
					pct = int(base * 100 / total)
				}
				reportStage("Downloading image", "Network interrupted - retrying…", pct)
				// On a prolonged outage the lease may be gone, not just the
				// connection: try to bring the link back up and re-DHCP.
				if attempt%3 == 0 {
					reacquireNetwork(ctx, log)
				}
			})
		if err != nil {
			return fmt.Errorf("file %s: %w", mf.Path, err)
		}
		done += mf.Size
		if total > 0 {
			reportStage("Downloading image", "Copying install media to disk",
				int(done*100/total))
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
	return c.DownloadFile(ctx, it.URL, dst, it.Size, payloadStallBudget,
		func(n int64) {
			log.Info("download.progress",
				slog.String("role", it.Role),
				slog.String("url", it.URL),
				slog.Int64("bytes", n))
		},
		func(attempt int, derr error) {
			log.Warn("download.retry",
				slog.String("role", it.Role),
				slog.Int("attempt", attempt),
				slog.String("error", derr.Error()))
			reportStage("Preparing", "Network interrupted - retrying…", -1)
			reportFile(itemLabel(it))
			if attempt%3 == 0 {
				reacquireNetwork(ctx, log)
			}
		})
}

// agentUpdateInfo mirrors the server's /api/v1/agent/update-info response.
type agentUpdateInfo struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
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
	reportFile("management agent")
	// The agent is optional, so it gets the shortest budget: ride out a brief
	// blip, but don't stall the deploy waiting on it -- give up and proceed
	// agent-less if the network is down for long.
	if err := c.DownloadFile(ctx, info.URL, dst, 0, agentStallBudget, nil,
		func(attempt int, derr error) {
			log.Warn("agent.retry", slog.Int("attempt", attempt), slog.String("error", derr.Error()))
			reportStage("Preparing", "Network interrupted - retrying…", -1)
			reportFile("management agent")
		}); err != nil {
		log.Warn("agent.download", slog.String("url", info.URL), slog.String("error", err.Error()))
		return ""
	}
	// Guard against a non-binary body (e.g. an auth redirect's login-page
	// HTML) being injected as the agent. A Windows PE starts with "MZ".
	if !looksLikePE(dst) {
		log.Warn("agent.invalid",
			slog.String("url", info.URL),
			slog.String("reason", "downloaded agent is not a PE binary; refusing to inject it"))
		return ""
	}
	// Verify the SHA-256 hash if the server provided one; otherwise log a
	// warning but don't break the flow (older servers may not serve the hash).
	if info.SHA256 != "" {
		actual, err := fileSHA256(dst)
		if err != nil {
			log.Warn("agent.hash.read", slog.String("error", err.Error()))
			return ""
		}
		if !strings.EqualFold(actual, info.SHA256) {
			log.Warn("agent.hash.mismatch",
				slog.String("url", info.URL),
				slog.String("expected", info.SHA256),
				slog.String("actual", actual),
				slog.String("reason", "SHA-256 mismatch; refusing to inject agent"))
			return ""
		}
		log.Info("agent.hash.ok", slog.String("sha256", actual))
	} else {
		log.Warn("agent.hash.skipped",
			slog.String("url", info.URL),
			slog.String("reason", "server did not provide a SHA-256 hash; skipping verification"))
	}
	log.Info("agent.fetched", slog.String("url", info.URL))
	return dst
}

// looksLikePE reports whether the file at path begins with the DOS/PE
// "MZ" magic. Cheap sanity check so a server error page never ships as the
// agent.
func looksLikePE(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [2]byte
	if _, err := f.Read(hdr[:]); err != nil {
		return false
	}
	return hdr[0] == 'M' && hdr[1] == 'Z'
}

// fileSHA256 returns the lowercase hex-encoded SHA-256 digest of the file at
// path. Used to verify agent binary downloads against the server-provided hash.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

// callbackScriptTemplate is adcb.ps1, the install-status milestone reporter
// staged into C:\Windows\Setup\Scripts via the $OEM$ tree. The generated
// unattend invokes it at the specialize and first-logon passes as
// `powershell -File ...\adcb.ps1 <server> <uuid> <status>`. It is fully
// static -- every per-machine value arrives as an argument -- and strictly
// best-effort: an 8s timeout, the whole body wrapped in try/catch, and an
// unconditional `exit 0`, so an unreachable or slow server never stalls or
// fails the Windows Setup pass it runs in. It accepts any server certificate
// to mirror the boot client's self-signed-TLS trust (Windows Setup ships
// PowerShell 5.1, whose Invoke-RestMethod lacks -SkipCertificateCheck).
const callbackScriptTemplate = "param($Server,$Uuid,$Status)\r\n" +
	"# Generated by AutoDeploy. Best-effort install-status milestone report.\r\n" +
	"try {\r\n" +
	"  try { [Net.ServicePointManager]::ServerCertificateValidationCallback = { $true } } catch {}\r\n" +
	"  try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}\r\n" +
	"  $body = @{ identity = @{ system_uuid = $Uuid }; status = $Status } | ConvertTo-Json -Compress\r\n" +
	"  $uri = $Server.TrimEnd('/') + '/api/v1/clients/deploy-status'\r\n" +
	"  Invoke-RestMethod -Uri $uri -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 8 | Out-Null\r\n" +
	"} catch {}\r\n" +
	"exit 0\r\n"

// writeCallbackScript writes adcb.ps1 to work and returns its path. The
// script is static (the unattend supplies server/uuid/status as args), so
// there is nothing to substitute or sanitise here.
func writeCallbackScript(work string) (string, error) {
	dst := filepath.Join(work, "adcb.ps1")
	if err := os.WriteFile(dst, []byte(callbackScriptTemplate), 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// sanitizeCmdValue validates that a value is safe for embedding in a
// Windows batch file (SetupComplete.cmd). Only characters that are safe
// in server URLs and agent IDs are allowed: alphanumeric, plus the set
// / : - . _ (covers https://host:port/path and UUID-style agent IDs).
// Returns an error if the value contains anything else, preventing
// cmd.exe metacharacter injection (&, |, >, <, ^, %, !, etc.).
func sanitizeCmdValue(name, value string) error {
	for i, c := range value {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '/' || c == ':' || c == '-' || c == '.' || c == '_':
			// safe
		default:
			return fmt.Errorf("%s contains unsafe character %q at position %d", name, string(c), i)
		}
	}
	return nil
}

// writeSetupComplete renders SetupComplete.cmd with the server URL and the
// machine's agent_id baked in, and writes it to work, returning its path.
func writeSetupComplete(work, serverURL, agentID string) (string, error) {
	if err := sanitizeCmdValue("server URL", serverURL); err != nil {
		return "", err
	}
	if err := sanitizeCmdValue("agent ID", agentID); err != nil {
		return "", err
	}
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

// submitPIN validates one PIN attempt against the server. An empty pin is
// the initial probe that returns granted=true when no PIN is configured.
// Returns (granted, locked). Shared by the console and GUI access gates so
// the server remains the sole authority on PIN correctness.
func submitPIN(ctx context.Context, c *httpc.Client, id smbios.Identity, pin string) (granted, locked bool) {
	var r struct {
		Granted   bool `json:"granted"`
		LockedOut bool `json:"locked_out,omitempty"`
	}
	if err := c.PostJSON(ctx, "/api/v1/clients/validate-pin", map[string]any{
		"system_uuid": id.SystemUUID,
		"pin":         pin,
	}, &r); err != nil {
		return false, false // fail-safe: treat as not granted
	}
	return r.Granted, r.LockedOut
}

// progressSink receives deploy stage updates so an interactive UI can show
// them. nil = no UI attached (the console deploy logs to slog as before).
type progressSink interface {
	Stage(stage, detail string, percent int)
	File(name string)
}

var activeProgress progressSink

// setProgressSink installs (or clears, with nil) the sink runDeploy reports
// stages to. Set by the GUI flow around a deploy.
func setProgressSink(p progressSink) { activeProgress = p }

// reportStage forwards a deploy stage to the active UI sink, if any. A
// negative percent means "indeterminate".
func reportStage(stage, detail string, percent int) {
	if activeProgress != nil {
		activeProgress.Stage(stage, detail, percent)
	}
}

// reportFile updates the "current artifact" line on the active UI sink with
// the name of the file being fetched, so an operator can see which file a
// deploy is on -- and which one a stall is pinned to. Pass a bare filename
// or a media-relative path -- NEVER a URL: the progress screen is shown in
// the open during imaging and must not reveal the server address.
func reportFile(name string) {
	if activeProgress != nil {
		activeProgress.File(name)
	}
}

// itemLabel returns a human, URL-free label for a manifest item, for the
// progress screen's current-file line. It uses the server-supplied display
// name and never the item URL.
func itemLabel(it manifestItem) string {
	if it.Name != "" {
		return it.Name
	}
	switch it.Role {
	case "driver":
		return "driver package"
	case "unattend":
		return "answer file"
	case "iso-media":
		return "install media"
	default:
		return it.Role
	}
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
	PrimaryColor     string `json:"primary_color"`
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

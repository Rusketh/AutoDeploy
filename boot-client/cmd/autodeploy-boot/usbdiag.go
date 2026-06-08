package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// logUSBNICDiagnostics records, for every USB Ethernet adapter, the facts that
// tell us WHY a transfer stalls under Linux when the same adapter pulled the
// kernel + initrd fine under iPXE/firmware: which driver is bound (r8152 vs a
// degraded CDC fallback), the USB link speed (480 = USB 2.0, 5000 = USB 3.0),
// MTU/carrier, the adapter's own error/drop/fifo counters, and recent kernel
// (dmesg) lines about it. These ship with the boot logs, so a stall becomes
// evidence instead of a guess. Best-effort and Linux-only.
func logUSBNICDiagnostics(ctx context.Context, log *slog.Logger) {
	logUSBNICDiagnosticsAt(ctx, log, "/sys")
}

func logUSBNICDiagnosticsAt(ctx context.Context, log *slog.Logger, sysfsRoot string) {
	for _, ifc := range listNetInterfaces(sysfsRoot) {
		if _, isUSB := usbNetDevicePath(sysfsRoot, ifc); !isUSB {
			continue
		}
		log.Info("net.usbnic.diag",
			slog.String("if", ifc),
			slog.String("driver", nicDriver(sysfsRoot, ifc)),
			slog.String("usb_speed_mbps", usbLinkSpeedMbps(sysfsRoot, ifc)),
			slog.String("mtu", readNetAttr(sysfsRoot, ifc, "mtu")),
			slog.String("operstate", readNetAttr(sysfsRoot, ifc, "operstate")),
			slog.String("carrier", readNetAttr(sysfsRoot, ifc, "carrier")),
		)
		// Per-adapter error counters (best-effort; needs ethtool). The ones
		// that move when an RTL8153 chokes a bulk transfer are rx_fifo/err/
		// dropped/missed, so surface only those rather than the full dump.
		if out, err := exec.CommandContext(ctx, "ethtool", "-S", ifc).Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				l := strings.TrimSpace(line)
				if l != "" && statCounterRelevant(l) {
					log.Info("net.usbnic.stat", slog.String("if", ifc), slog.String("counter", l))
				}
			}
		}
	}
	// Kernel ring buffer lines about USB networking -- r8152 resets, USB
	// disconnects, link flaps. Tail the matches so the log stays small.
	if out, err := exec.CommandContext(ctx, "dmesg").Output(); err == nil {
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		var kept []string
		for _, l := range lines {
			if dmesgRelevant(l) {
				kept = append(kept, l)
			}
		}
		if len(kept) > 40 {
			kept = kept[len(kept)-40:]
		}
		for _, l := range kept {
			log.Info("net.usbnic.dmesg", slog.String("line", strings.TrimSpace(l)))
		}
	}
}

// nicDriver returns the kernel driver bound to interface ifc (e.g. "r8152" or
// "cdc_ether"), or "" if none/unknown. The diagnostic that matters most: a
// Realtek adapter on "cdc_ether" is the degraded path that stalls.
func nicDriver(sysfsRoot, ifc string) string {
	p, err := filepath.EvalSymlinks(filepath.Join(sysfsRoot, "class", "net", ifc, "device", "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(p)
}

// usbLinkSpeedMbps returns the USB link speed in Mbps for the USB NIC backing
// ifc, read from the nearest "speed" attribute up its sysfs chain (480 = USB
// 2.0, 5000 = USB 3.0). Empty when not a USB device or unreadable.
func usbLinkSpeedMbps(sysfsRoot, ifc string) string {
	dev, isUSB := usbNetDevicePath(sysfsRoot, ifc)
	if !isUSB {
		return ""
	}
	for p := dev; strings.Contains(p, "usb"); {
		if b, err := os.ReadFile(filepath.Join(p, "speed")); err == nil {
			return strings.TrimSpace(string(b))
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return ""
}

// readNetAttr reads a single-line sysfs attribute of a net interface.
func readNetAttr(sysfsRoot, ifc, attr string) string {
	b, err := os.ReadFile(filepath.Join(sysfsRoot, "class", "net", ifc, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// statCounterRelevant keeps only the ethtool -S counters worth logging for a
// stalled transfer: errors, drops, fifo overruns, misses, resets.
func statCounterRelevant(line string) bool {
	l := strings.ToLower(line)
	for _, k := range []string{"err", "drop", "fifo", "miss", "reset", "over", "abort"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// dmesgRelevant reports whether a kernel log line concerns USB networking, so
// the diagnostics keep r8152/USB events and drop the noise.
func dmesgRelevant(line string) bool {
	l := strings.ToLower(line)
	for _, k := range []string{"r8152", "r8153", "cdc_ether", "cdc_ncm", "ax88179", "asix", "usbnet"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	if strings.Contains(l, "usb") &&
		(strings.Contains(l, "reset") || strings.Contains(l, "disconnect") ||
			strings.Contains(l, "error") || strings.Contains(l, "timeout")) {
		return true
	}
	return strings.Contains(l, "link is") // "Link is Up/Down"
}

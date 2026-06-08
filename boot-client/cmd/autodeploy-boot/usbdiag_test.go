package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNICDriverAndUSBSpeed(t *testing.T) {
	root := canonRoot(t)
	// USB interface dir with a 'driver' symlink -> .../drivers/r8152, and a
	// 'speed' attribute on the USB device dir above it (USB 3.0 = 5000).
	usbIf := filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-1", "1-1:1.0")
	if err := os.MkdirAll(usbIf, 0o755); err != nil {
		t.Fatal(err)
	}
	drvDir := filepath.Join(root, "bus", "usb", "drivers", "r8152")
	if err := os.MkdirAll(drvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(drvDir, filepath.Join(usbIf, "driver")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-1", "speed"), []byte("5000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeNet(t, root, "eth0", usbIf)
	if err := os.WriteFile(filepath.Join(root, "class", "net", "eth0", "mtu"), []byte("1500\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := nicDriver(root, "eth0"); got != "r8152" {
		t.Errorf("nicDriver = %q, want r8152", got)
	}
	if got := usbLinkSpeedMbps(root, "eth0"); got != "5000" {
		t.Errorf("usbLinkSpeedMbps = %q, want 5000", got)
	}
	if got := readNetAttr(root, "eth0", "mtu"); got != "1500" {
		t.Errorf("readNetAttr(mtu) = %q, want 1500", got)
	}
}

// The degraded path the rebind targets: a Realtek adapter on the generic
// cdc_ether driver. nicDriver must surface it so the logs name the culprit.
func TestNICDriverReportsCDCFallback(t *testing.T) {
	root := canonRoot(t)
	usbIf := filepath.Join(root, "devices", "x", "usb2", "2-1", "2-1:1.0")
	if err := os.MkdirAll(usbIf, 0o755); err != nil {
		t.Fatal(err)
	}
	drvDir := filepath.Join(root, "bus", "usb", "drivers", "cdc_ether")
	if err := os.MkdirAll(drvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(drvDir, filepath.Join(usbIf, "driver")); err != nil {
		t.Fatal(err)
	}
	fakeNet(t, root, "eth5", usbIf)
	if got := nicDriver(root, "eth5"); got != "cdc_ether" {
		t.Errorf("nicDriver = %q, want cdc_ether", got)
	}
	// No driver symlink at all -> empty, not a crash.
	fakeNet(t, root, "eth6", filepath.Join(root, "devices", "x", "usb2", "2-2", "2-2:1.0"))
	_ = os.MkdirAll(filepath.Join(root, "devices", "x", "usb2", "2-2", "2-2:1.0"), 0o755)
	if got := nicDriver(root, "eth6"); got != "" {
		t.Errorf("nicDriver with no driver = %q, want empty", got)
	}
}

// usbAuthorizedPath must land on the USB device node (which carries the
// writable "authorized" used to replug it), not the interface node the netdev
// points at -- that's what makes the Dell-documented unplug/replug work.
func TestUSBAuthorizedPath(t *testing.T) {
	root := canonRoot(t)
	usbDev := filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-1")
	usbIf := filepath.Join(usbDev, "1-1:1.0")
	if err := os.MkdirAll(usbIf, 0o755); err != nil {
		t.Fatal(err)
	}
	// The device node carries idVendor + authorized; the interface node doesn't.
	for name, val := range map[string]string{"idVendor": "0bda\n", "authorized": "1\n"} {
		if err := os.WriteFile(filepath.Join(usbDev, name), []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeNet(t, root, "eth0", usbIf)

	if got := usbAuthorizedPath(root, "eth0"); got != usbDev {
		t.Errorf("usbAuthorizedPath = %q, want the device node %q", got, usbDev)
	}
	// A PCIe NIC (no USB chain) yields no authorized node.
	pci := filepath.Join(root, "devices", "pci0000:00", "0000:00:1f.6")
	if err := os.MkdirAll(pci, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNet(t, root, "eth1", pci)
	if got := usbAuthorizedPath(root, "eth1"); got != "" {
		t.Errorf("usbAuthorizedPath(PCIe) = %q, want empty", got)
	}
}

func TestStatCounterRelevant(t *testing.T) {
	for _, s := range []string{"rx_fifo_errors: 12", "tx_dropped: 3", "rx_missed: 1", "port_reset: 2", "rx_over_errors: 0", "tx_aborted: 1"} {
		if !statCounterRelevant(s) {
			t.Errorf("want relevant: %q", s)
		}
	}
	for _, s := range []string{"rx_packets: 1000", "tx_bytes: 999", "collisions: 0"} {
		if statCounterRelevant(s) {
			t.Errorf("want irrelevant: %q", s)
		}
	}
}

func TestDmesgRelevant(t *testing.T) {
	for _, s := range []string{
		"[ 12.3] r8152 2-1:1.0 eth0: Tx timeout",
		"[ 10.0] usb 2-1: reset SuperSpeed USB device number 3",
		"[  9.9] cdc_ether 1-1:1.0 eth0: register 'cdc_ether'",
		"[ 20.1] eth0: Link is Down",
	} {
		if !dmesgRelevant(s) {
			t.Errorf("want relevant: %q", s)
		}
	}
	for _, s := range []string{"[ 1.0] EXT4-fs (sda1): mounted", "[ 2.0] random: crng init done"} {
		if dmesgRelevant(s) {
			t.Errorf("want irrelevant: %q", s)
		}
	}
}

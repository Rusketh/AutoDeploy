package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeNet wires a fake <root>/class/net/<ifc>/device symlink pointing at
// devAbs (an absolute path under root, created by the caller). It mirrors how
// real sysfs links a netdev to its backing bus device.
func fakeNet(t *testing.T, root, ifc, devAbs string) {
	t.Helper()
	ifDir := filepath.Join(root, "class", "net", ifc)
	if err := os.MkdirAll(ifDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(devAbs, filepath.Join(ifDir, "device")); err != nil {
		t.Fatal(err)
	}
}

// seedControl creates dir/power/control holding "auto" (the autosuspend-on
// default) and returns its path, so a test can assert it was flipped to "on".
func seedControl(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "power", "control")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("auto"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// canonRoot returns a symlink-resolved temp dir, so paths built from it match
// what filepath.EvalSymlinks yields for the netdev links (relevant where /tmp
// is itself a symlink).
func canonRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUSBNetDevicePathDetectsBus(t *testing.T) {
	root := canonRoot(t)

	// A USB NIC: its device resolves under a usbN host controller path.
	usbDev := filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-1", "1-1:1.0")
	if err := os.MkdirAll(usbDev, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNet(t, root, "eth0", usbDev)

	// A PCIe NIC: no "usb" component anywhere in the resolved path.
	pciDev := filepath.Join(root, "devices", "pci0000:00", "0000:00:1f.6")
	if err := os.MkdirAll(pciDev, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNet(t, root, "eth1", pciDev)

	// Loopback: no device link at all.
	if err := os.MkdirAll(filepath.Join(root, "class", "net", "lo"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, isUSB := usbNetDevicePath(root, "eth0"); !isUSB || got != usbDev {
		t.Errorf("eth0: got (%q, %v), want (%q, true)", got, isUSB, usbDev)
	}
	if _, isUSB := usbNetDevicePath(root, "eth1"); isUSB {
		t.Error("eth1 (PCIe) should not be detected as USB")
	}
	if got, isUSB := usbNetDevicePath(root, "lo"); isUSB || got != "" {
		t.Errorf("lo: got (%q, %v), want (\"\", false)", got, isUSB)
	}
}

func TestListNetInterfacesSkipsLoopback(t *testing.T) {
	root := canonRoot(t)
	for _, ifc := range []string{"eth0", "eth1", "lo"} {
		if err := os.MkdirAll(filepath.Join(root, "class", "net", ifc), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := listNetInterfaces(root)
	want := map[string]bool{"eth0": true, "eth1": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want the two non-loopback interfaces", got)
	}
	for _, ifc := range got {
		if ifc == "lo" {
			t.Error("listNetInterfaces must skip lo")
		}
		if !want[ifc] {
			t.Errorf("unexpected interface %q", ifc)
		}
	}

	// A missing class/net directory is empty, not an error.
	if got := listNetInterfaces(filepath.Join(root, "nope")); got != nil {
		t.Errorf("absent sysfs root should yield nil, got %v", got)
	}
}

func TestDisableAutosuspendWalksChain(t *testing.T) {
	root := canonRoot(t)
	// The xHCI host controller is PCI (no "usb"); the chain above the netdev
	// is its interface, the USB device, and the root hub -- all "usb".
	hostCtl := filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0")
	rootHub := filepath.Join(hostCtl, "usb1")
	usbDev := filepath.Join(rootHub, "1-1")
	usbIf := filepath.Join(usbDev, "1-1:1.0")

	ifCtrl := seedControl(t, usbIf)
	devCtrl := seedControl(t, usbDev)
	hubCtrl := seedControl(t, rootHub)
	hostCtrl := seedControl(t, hostCtl) // PCI parent -- must be left alone

	if n := disableUSBAutosuspend(usbIf); n != 3 {
		t.Errorf("flipped %d power/control files, want 3 (interface, device, root hub)", n)
	}
	for _, p := range []string{ifCtrl, devCtrl, hubCtrl} {
		if got := mustRead(t, p); got != "on" {
			t.Errorf("%s = %q, want \"on\"", p, got)
		}
	}
	// Walking must stop when the path leaves USB: the PCI host controller's
	// control is untouched.
	if got := mustRead(t, hostCtrl); got != "auto" {
		t.Errorf("PCI host controller control = %q, want untouched \"auto\"", got)
	}
}

func TestDisableAutosuspendNonUSBNoop(t *testing.T) {
	root := canonRoot(t)
	pciDev := filepath.Join(root, "devices", "pci0000:00", "0000:00:1f.6")
	ctrl := seedControl(t, pciDev)
	if n := disableUSBAutosuspend(pciDev); n != 0 {
		t.Errorf("non-USB path flipped %d files, want 0", n)
	}
	if got := mustRead(t, ctrl); got != "auto" {
		t.Errorf("non-USB control = %q, want untouched \"auto\"", got)
	}
}

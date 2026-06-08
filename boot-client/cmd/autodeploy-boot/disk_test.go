package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// testLogger returns a logger that discards output -- the detection functions
// log diagnostics we don't assert on here.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDisk wires a fake <root>/block/<name> with removable + size attributes
// and, when devAbs is non-empty, a device symlink (used to mark USB-attached
// disks the way real sysfs links a block device to its backing bus device).
func fakeDisk(t *testing.T, root, name, removable, size, devAbs string) {
	t.Helper()
	dir := filepath.Join(root, "block", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "removable"), []byte(removable+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "size"), []byte(size+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if devAbs != "" {
		if err := os.MkdirAll(devAbs, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(devAbs, filepath.Join(dir, "device")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectInternalDisksPrefersNVMeAndSkipsUSBAndRemovable(t *testing.T) {
	root := canonRoot(t)

	// NVMe OS disk on PCIe -- the preferred target.
	fakeDisk(t, root, "nvme0n1", "0", "1000215216",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:1d.0", "nvme", "nvme0"))
	// A SATA data disk -- eligible, but ranked below NVMe.
	fakeDisk(t, root, "sda", "0", "3907029168",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:17.0", "ata1", "host0"))
	// A USB SSD/stick (resolves through a usb component) -- must be skipped.
	fakeDisk(t, root, "sdb", "0", "60088320",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-1"))
	// A removable card reader -- must be skipped.
	fakeDisk(t, root, "mmcblk0", "1", "31116288", "")
	// An optical drive and a loop device -- skipped by name.
	fakeDisk(t, root, "sr0", "1", "0", "")
	fakeDisk(t, root, "loop0", "0", "204800", "")

	got := detectInternalDisks(root, "/dev")
	want := []string{"/dev/nvme0n1", "/dev/sda"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectInternalDisks = %v, want %v", got, want)
	}
}

func TestDetectInternalDisksSingleSATA(t *testing.T) {
	root := canonRoot(t)
	// The historical common case: one SATA disk, no NVMe -> /dev/sda, exactly
	// the old hard-coded default.
	fakeDisk(t, root, "sda", "0", "500118192",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:17.0", "ata1"))
	got := detectInternalDisks(root, "/dev")
	if want := []string{"/dev/sda"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detectInternalDisks = %v, want %v", got, want)
	}
}

func TestDetectInternalDisksEMMC(t *testing.T) {
	root := canonRoot(t)
	// Onboard eMMC (non-removable) is the only disk on machines like the Dell
	// Latitude 3190; it must be detected. The kernel also exposes the eMMC
	// boot/RPMB hardware areas as their own block devices -- those must be
	// skipped so only the main user-data device is returned.
	mmcHost := filepath.Join(root, "devices", "pci0000:00", "0000:00:1a.0", "mmc_host", "mmc0")
	fakeDisk(t, root, "mmcblk0", "0", "61071360", mmcHost)
	fakeDisk(t, root, "mmcblk0boot0", "0", "8192", mmcHost)
	fakeDisk(t, root, "mmcblk0boot1", "0", "8192", mmcHost)
	fakeDisk(t, root, "mmcblk0rpmb", "0", "8192", mmcHost)
	if got := detectInternalDisks(root, "/dev"); !reflect.DeepEqual(got, []string{"/dev/mmcblk0"}) {
		t.Fatalf("eMMC detect = %v, want [/dev/mmcblk0] (boot/rpmb areas excluded)", got)
	}
	// An NVMe is still preferred over eMMC when both are present.
	fakeDisk(t, root, "nvme0n1", "0", "1000215216",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:1d.0", "nvme", "nvme0"))
	if got := detectInternalDisks(root, "/dev"); !reflect.DeepEqual(got, []string{"/dev/nvme0n1", "/dev/mmcblk0"}) {
		t.Fatalf("nvme+eMMC detect = %v, want [/dev/nvme0n1 /dev/mmcblk0]", got)
	}
}

func TestDetectInternalDisksNoneAndAbsent(t *testing.T) {
	root := canonRoot(t)
	// Only ineligible/removable devices present -> no candidates.
	fakeDisk(t, root, "sr0", "1", "0", "")
	fakeDisk(t, root, "sdb", "1", "60088320", "") // removable USB stick
	if got := detectInternalDisks(root, "/dev"); got != nil && len(got) != 0 {
		t.Errorf("expected no candidates, got %v", got)
	}
	// An absent /sys/block yields nil, not an error.
	if got := detectInternalDisks(filepath.Join(root, "nope"), "/dev"); got != nil {
		t.Errorf("absent block root should yield nil, got %v", got)
	}
}

func TestResolveTargetDiskExplicitMissingDoesNotGuess(t *testing.T) {
	root := canonRoot(t)
	// A perfectly good internal disk is present...
	fakeDisk(t, root, "nvme0n1", "0", "1000215216",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:1d.0", "nvme", "nvme0"))
	// ...but the operator explicitly asked for one that doesn't exist. We must
	// NOT silently fall back and wipe the NVMe -- return "" so the caller fails
	// safe.
	got := resolveTargetDisk(filepath.Join(root, "does-not-exist"), root, "/dev", testLogger())
	if got != "" {
		t.Errorf("explicit missing disk should resolve to \"\", got %q", got)
	}
}

func TestResolveTargetDiskExplicitPresentHonoured(t *testing.T) {
	root := canonRoot(t)
	// Use a path that exists (the temp dir itself stands in for a device node).
	got := resolveTargetDisk(root, root, "/dev", testLogger())
	if got != root {
		t.Errorf("explicit present disk should be honoured: got %q want %q", got, root)
	}
}

func TestResolveTargetDiskAutoDetects(t *testing.T) {
	root := canonRoot(t)
	fakeDisk(t, root, "nvme0n1", "0", "1000215216",
		filepath.Join(root, "devices", "pci0000:00", "0000:00:1d.0", "nvme", "nvme0"))
	got := resolveTargetDisk("", root, "/dev", testLogger())
	if got != "/dev/nvme0n1" {
		t.Errorf("auto-detect should pick /dev/nvme0n1, got %q", got)
	}
}

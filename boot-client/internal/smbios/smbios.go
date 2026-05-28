// Package smbios reads hardware identity from SMBIOS/DMI tables exposed by
// firmware. The pre-boot environment cannot use WMIC; identity must come
// from firmware tables directly.
//
// Phase 0 ships only the type definitions and a sysfs-based reader that
// works on Linux (which is what the Boot Client runs on). The full parser
// for Type 0/1/2 structures lands in Phase 3 when the Boot Client actually
// touches hardware.
package smbios

import (
	"os"
	"path/filepath"
	"strings"
)

// Identity holds the hardware-identity facts reported to the server.
type Identity struct {
	// Type 1 — System.
	SystemManufacturer string `json:"system_manufacturer"`
	SystemProduct      string `json:"system_product"`
	SystemSerial       string `json:"system_serial"`
	SystemUUID         string `json:"system_uuid"`

	// Type 0 — BIOS.
	BIOSVendor      string `json:"bios_vendor"`
	BIOSVersion     string `json:"bios_version"`
	BIOSReleaseDate string `json:"bios_release_date"`

	// Type 2 — Baseboard.
	BoardManufacturer string `json:"board_manufacturer"`
	BoardProduct      string `json:"board_product"`
	BoardSerial       string `json:"board_serial"`
}

// ReadFromSysfs reads identity from /sys/class/dmi/id/* on Linux. This is the
// pre-boot environment's normal source.
func ReadFromSysfs(root string) (Identity, error) {
	if root == "" {
		root = "/sys/class/dmi/id"
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return Identity{
		SystemManufacturer: read("sys_vendor"),
		SystemProduct:      read("product_name"),
		SystemSerial:       read("product_serial"),
		SystemUUID:         read("product_uuid"),
		BIOSVendor:         read("bios_vendor"),
		BIOSVersion:        read("bios_version"),
		BIOSReleaseDate:    read("bios_date"),
		BoardManufacturer:  read("board_vendor"),
		BoardProduct:       read("board_name"),
		BoardSerial:        read("board_serial"),
	}, nil
}

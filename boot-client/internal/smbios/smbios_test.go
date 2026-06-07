package smbios

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFromSysfs(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"sys_vendor":     "ACME\n",
		"product_name":   "Lab-Rig 9000\n",
		"product_serial": "SN12345\n",
		"product_uuid":   "1234-uuid\n",
		"bios_vendor":    "Phoenix\n",
		"bios_version":   "1.0\n",
		"bios_date":      "01/01/2026\n",
		"board_vendor":   "ACME-Boards\n",
		"board_name":     "X1\n",
		"board_serial":   "BSN9\n",
	}
	for n, v := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id, err := ReadFromSysfs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id.SystemUUID != "1234-uuid" {
		t.Errorf("UUID = %q", id.SystemUUID)
	}
	if id.SystemManufacturer != "ACME" {
		t.Errorf("Manufacturer = %q", id.SystemManufacturer)
	}
	if id.BoardSerial != "BSN9" {
		t.Errorf("BoardSerial = %q", id.BoardSerial)
	}
}

func TestReadFromSysfsMissingDir(t *testing.T) {
	// A missing sysfs dir produces an empty UUID, which is now an error
	// since UUID is required for inventory tracking.
	_, err := ReadFromSysfs(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing sysfs dir (empty UUID)")
	}
}

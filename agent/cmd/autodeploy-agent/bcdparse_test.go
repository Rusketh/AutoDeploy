package main

import "testing"

func TestFindNetworkFirmwareEntry(t *testing.T) {
	enum := `
Firmware Boot Manager
---------------------
identifier              {fwbootmgr}
displayorder            {bootmgr}
                        {b1a2c3d4-0000-0000-0000-000000000001}

Windows Boot Manager
--------------------
identifier              {bootmgr}
description             Windows Boot Manager

Firmware Application (101fffff)
-------------------------------
identifier              {b1a2c3d4-0000-0000-0000-000000000001}
description             UEFI: PXE IPv4 Intel(R) Ethernet Connection

Firmware Application (101fffff)
-------------------------------
identifier              {b1a2c3d4-0000-0000-0000-000000000002}
description             UEFI: Hard Disk
`
	if got, want := findNetworkFirmwareEntry(enum), "{b1a2c3d4-0000-0000-0000-000000000001}"; got != want {
		t.Errorf("findNetworkFirmwareEntry = %q, want %q", got, want)
	}

	// No network entry -> empty (caller falls back to firmware boot order).
	none := `
identifier              {bootmgr}
description             Windows Boot Manager
identifier              {aaaa1111-0000-0000-0000-000000000001}
description             UEFI: Hard Disk
`
	if got := findNetworkFirmwareEntry(none); got != "" {
		t.Errorf("expected no network entry, got %q", got)
	}

	// A "Windows" description is never the network entry even if it
	// contains a matching word.
	winOnly := `
identifier              {bootmgr}
description             Windows Boot Manager Network Service
`
	if got := findNetworkFirmwareEntry(winOnly); got != "" {
		t.Errorf("Windows entry should be skipped, got %q", got)
	}

	// Vendor variant phrasing (Intel Boot Agent).
	iba := `
identifier              {cccc2222-0000-0000-0000-000000000003}
description             IBA GE Slot 00C8 v1550
`
	if got, want := findNetworkFirmwareEntry(iba), "{cccc2222-0000-0000-0000-000000000003}"; got != want {
		t.Errorf("IBA entry: got %q, want %q", got, want)
	}
}

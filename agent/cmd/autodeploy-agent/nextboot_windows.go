//go:build windows

package main

import (
	"context"
	"os/exec"
	"time"
)

// setNextBootNetwork sets a ONE-TIME UEFI next-boot to the machine's
// network/PXE firmware entry, so a re-image reboot lands in AutoDeploy even
// when the firmware's permanent boot order puts Windows first. It does NOT
// change the persistent boot order -- `bcdedit {fwbootmgr} bootsequence` is
// consumed on the next boot, so a failed/again-Windows boot afterwards is
// normal.
//
// Best-effort: returns a short status. If no network entry is found (or
// bcdedit fails), the caller still reboots and relies on firmware boot
// order -- exactly the previous behaviour.
func setNextBootNetwork() string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bcdedit", "/enum", "firmware").CombinedOutput()
	if err != nil {
		return "error: bcdedit enum: " + err.Error()
	}
	guid := findNetworkFirmwareEntry(string(out))
	if guid == "" {
		return "no-network-entry"
	}
	if err := exec.CommandContext(ctx, "bcdedit", "/set", "{fwbootmgr}", "bootsequence", guid).Run(); err != nil {
		return "error: set bootsequence: " + err.Error()
	}
	return "next-boot=" + guid
}

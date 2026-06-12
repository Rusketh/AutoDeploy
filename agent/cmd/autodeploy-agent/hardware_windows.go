//go:build windows

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// collectHardware gathers CPU/RAM/disk/GPU/NIC facts via a single
// PowerShell CIM query and returns them as the map the server stores. Each
// field is best-effort -- a query that fails just leaves that field empty.
func collectHardware() map[string]any {
	hw := map[string]any{
		"reported_at": time.Now().UTC(),
	}
	// One PowerShell invocation emits a JSON object with everything, so we
	// pay the interpreter startup cost once rather than per class.
	const script = `
$ErrorActionPreference='SilentlyContinue'
$cs  = Get-CimInstance Win32_ComputerSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$os  = Get-CimInstance Win32_OperatingSystem
$disks = Get-CimInstance Win32_DiskDrive | ForEach-Object { @{ model=$_.Model; size_bytes=[int64]$_.Size } }
$gpus  = Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name }
$nics  = Get-CimInstance Win32_NetworkAdapterConfiguration -Filter 'IPEnabled=true' | ForEach-Object {
    @{ name=$_.Description; mac=$_.MACAddress; ips=@($_.IPAddress) }
}
[pscustomobject]@{
    cpu_model    = $cpu.Name
    cpu_cores    = [int]$cpu.NumberOfCores
    cpu_threads  = [int]$cpu.NumberOfLogicalProcessors
    memory_bytes = [int64]$cs.TotalPhysicalMemory
    disks        = @($disks)
    gpus         = @($gpus)
    nics         = @($nics)
    os_caption   = $os.Caption
    os_version   = $os.Version
} | ConvertTo-Json -Depth 5 -Compress`

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).Output()
	if err != nil {
		return hw
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &parsed); err != nil {
		return hw
	}
	for k, v := range parsed {
		// Drop nulls so the server stores only what we actually read.
		if v != nil {
			hw[k] = v
		}
	}
	return hw
}

// collectSMBIOSIdentity reads the machine's SMBIOS identity (make/model,
// serial, SKU/family, BIOS, base board) via CIM, keyed the way the server's
// match.Identity expects. The PXE path reports these from firmware tables
// pre-boot, but a manually-installed agent is the machine's only voice —
// without this its inventory record never gains the make/model/board facts
// that driver filters (and the portal's filter helpers) need. The system
// UUID is deliberately NOT read here; the caller injects the one it already
// trusts. Returns nil if the query fails; each field is best-effort.
func collectSMBIOSIdentity() map[string]any {
	const script = `
$ErrorActionPreference='SilentlyContinue'
$cs   = Get-CimInstance Win32_ComputerSystem
$bios = Get-CimInstance Win32_BIOS
$bb   = Get-CimInstance Win32_BaseBoard
[pscustomobject]@{
    system_manufacturer = "$($cs.Manufacturer)"
    system_product      = "$($cs.Model)"
    system_serial       = "$($bios.SerialNumber)"
    system_sku          = "$($cs.SystemSKUNumber)"
    system_family       = "$($cs.SystemFamily)"
    bios_vendor         = "$($bios.Manufacturer)"
    bios_version        = "$($bios.SMBIOSBIOSVersion)"
    board_manufacturer  = "$($bb.Manufacturer)"
    board_product       = "$($bb.Product)"
    board_serial        = "$($bb.SerialNumber)"
} | ConvertTo-Json -Compress`

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).Output()
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &parsed); err != nil {
		return nil
	}
	id := map[string]any{}
	for k, v := range parsed {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			id[k] = strings.TrimSpace(s)
		}
	}
	return id
}

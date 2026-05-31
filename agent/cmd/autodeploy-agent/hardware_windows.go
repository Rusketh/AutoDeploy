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

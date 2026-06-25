//go:build windows

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// collectOSFacts reads the target's OS caption and version via a single CIM
// query. It deliberately uses Win32_OperatingSystem.Caption -- the same value
// the inventory stores and the portal shows (e.g. "Microsoft Windows 11 Pro")
// -- so a step's FilterOS ("Windows 11") matches the string operators see.
// Using the CIM caption rather than the registry's ProductName matters: on
// Windows 11 the registry ProductName still reads "Windows 10", which would
// make a "Windows 11" filter never match.
//
// Best-effort: on any failure both values come back empty, which makes every
// OS-filtered step skip (fail-closed) while unfiltered steps still run.
func collectOSFacts() (caption, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const script = `$o = Get-CimInstance Win32_OperatingSystem; ` +
		`[pscustomobject]@{ caption = $o.Caption; version = $o.Version } | ConvertTo-Json -Compress`
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).Output()
	if err != nil {
		return "", ""
	}
	var parsed struct {
		Caption string `json:"caption"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &parsed); err != nil {
		return "", ""
	}
	return parsed.Caption, parsed.Version
}

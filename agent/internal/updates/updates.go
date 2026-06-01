// Package updates installs Windows Update .msu files via dism.exe and
// scans for installed hotfixes via Get-HotFix.
package updates

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Install applies a .msu update package using dism.exe. The noRestart
// flag controls whether /NoRestart is passed (true = agent handles reboot
// separately based on the update's reboot_after flag).
func Install(ctx context.Context, msuPath string) (int, error) {
	cmd := exec.CommandContext(ctx, "dism.exe",
		"/Online", "/Add-Package",
		"/PackagePath:"+msuPath,
		"/Quiet", "/NoRestart")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), fmt.Errorf("dism.exe: exit %d: %s", ee.ExitCode(), string(out))
		}
		return -1, err
	}
	return 0, nil
}

// ScanInstalledKBs runs Get-HotFix via PowerShell and returns a list of
// installed KB numbers (e.g. ["KB5034441", "KB5033372"]).
func ScanInstalledKBs(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "Get-HotFix | Select-Object -ExpandProperty HotFixID")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Get-HotFix: %w: %s", err, string(out))
	}
	var kbs []string
	for _, line := range strings.Split(string(out), "\n") {
		kb := strings.TrimSpace(line)
		if kb != "" && strings.HasPrefix(strings.ToUpper(kb), "KB") {
			kbs = append(kbs, kb)
		}
	}
	return kbs, nil
}

//go:build windows

package detect

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// WindowsBackend is the real-host backend. File operations are stdlib; the
// registry and MSI queries shell out to reg.exe and PowerShell so we have
// no CGO dependency and no third-party Windows-API wrapper. These shells
// are present on every supported Windows version.
type WindowsBackend struct{}

func (WindowsBackend) FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (WindowsBackend) FileVersion(path string) (string, error) {
	// Use PowerShell's (Get-Item).VersionInfo.FileVersion for portability.
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive",
		"-Command",
		fmt.Sprintf(`(Get-Item -LiteralPath '%s').VersionInfo.FileVersion`,
			strings.ReplaceAll(path, "'", "''"))).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (WindowsBackend) RegistryValuePresent(hive, key, value string) (bool, string, error) {
	// reg query returns 0 on hit, 1 on miss; output contains the type and
	// value on hit.
	args := []string{"query", hive + `\` + key}
	if value != "" {
		args = append(args, "/v", value)
	}
	out, err := exec.Command("reg", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, "", nil
		}
		return false, "", err
	}
	if value == "" {
		return true, "", nil
	}
	// Parse the value off the last non-empty line: "    Name    TYPE    Value"
	v := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(value)) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			v = strings.Join(fields[2:], " ")
		}
		break
	}
	return true, v, nil
}

func (WindowsBackend) MSIProductInstalled(productCode string) (bool, error) {
	// Probe HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\<code>.
	out, err := exec.Command("reg", "query",
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`+productCode).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	return true, nil
}

// DefaultBackend returns the Windows backend on Windows builds.
func DefaultBackend() Backend { return WindowsBackend{} }

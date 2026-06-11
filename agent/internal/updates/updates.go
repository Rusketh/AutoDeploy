// Package updates installs Windows Update payloads — .msu via wusa.exe,
// .cab via dism.exe — and scans for installed hotfixes via Get-HotFix.
package updates

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallCommand picks the installer for an update payload by extension.
// .msu must go through wusa.exe: dism /Online /Add-Package does not accept
// .msu against a running OS before Windows 11 21H2, so it fails on every
// Windows 10 / Server release. .cab goes through dism. Anything else is
// rejected rather than mis-run.
func InstallCommand(path string) (name string, args []string, err error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".msu":
		return "wusa.exe", []string{path, "/quiet", "/norestart"}, nil
	case ".cab":
		return "dism.exe", []string{"/Online", "/Add-Package",
			"/PackagePath:" + path, "/Quiet", "/NoRestart"}, nil
	default:
		return "", nil, fmt.Errorf("unsupported update payload extension %q (.msu or .cab required)", filepath.Ext(path))
	}
}

// Outcome classifies an installer exit code.
type Outcome struct {
	Success        bool
	RebootRequired bool
	Reason         string // human-readable, e.g. "already installed"
}

// ClassifyExitCode maps wusa/dism exit codes to an outcome. Codes are
// normalized through uint32 so the Windows unsigned form (2149842967) and
// a sign-extended negative form (-2145124329) classify identically.
func ClassifyExitCode(code int) Outcome {
	switch uint32(code) {
	case 0:
		return Outcome{Success: true, Reason: "installed"}
	case 3010: // ERROR_SUCCESS_REBOOT_REQUIRED
		return Outcome{Success: true, RebootRequired: true, Reason: "installed, reboot required"}
	case 1641: // ERROR_SUCCESS_REBOOT_INITIATED
		return Outcome{Success: true, RebootRequired: true, Reason: "installed, reboot initiated"}
	case 0x00240005: // WU_S_REBOOT_REQUIRED
		return Outcome{Success: true, RebootRequired: true, Reason: "installed, reboot required"}
	case 0x00240006: // WU_S_ALREADY_INSTALLED
		return Outcome{Success: true, Reason: "already installed"}
	case 0x80070422:
		return Outcome{Reason: "0x80070422: Windows Update service is disabled"}
	case 0x80240017: // WU_E_NOT_APPLICABLE
		return Outcome{Reason: "0x80240017: update not applicable to this system"}
	case 0x800f081e: // CBS_E_NOT_APPLICABLE
		return Outcome{Reason: "0x800f081e: package not applicable (DISM)"}
	case 87: // ERROR_INVALID_PARAMETER
		return Outcome{Reason: "exit 87: invalid installer parameter"}
	default:
		return Outcome{Reason: fmt.Sprintf("exit %d (0x%08x)", code, uint32(code))}
	}
}

// Install runs the right installer for the payload at path and returns the
// raw exit code plus the combined output (tail-truncated for reporting).
// The error is non-nil only when the installer could not be run at all —
// non-zero exits come back as codes for the caller to classify.
func Install(ctx context.Context, path string) (int, string, error) {
	name, args, err := InstallCommand(path)
	if err != nil {
		return -1, "", err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	output := tailString(string(out), 2048)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), output, nil
		}
		return -1, output, fmt.Errorf("%s: %w", name, err)
	}
	return 0, output, nil
}

// tailString returns at most the last max bytes of s; installer output can
// be long and only the end carries the verdict.
func tailString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
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

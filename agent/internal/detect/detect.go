// Package detect evaluates SoftwarePackage detection rules on the agent
// host. A package is considered already installed when EVERY rule reports
// Present == true; zero rules means "always re-install".
//
// The package speaks through a Backend interface so unit tests can use a
// fake filesystem / registry / MSI provider without touching the real
// machine. The Windows backend (build-tag windows) wraps the real APIs;
// the portable backend used on Linux dev hosts returns "not detected" for
// registry/MSI rules so a package with those rules will always re-install,
// which is the safe default off-Windows.
package detect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// Backend abstracts the host-specific bits of detection.
type Backend interface {
	FileExists(path string) (bool, error)
	FileVersion(path string) (string, error)
	RegistryValuePresent(hive, key, value string) (bool, string, error)
	MSIProductInstalled(productCode string) (bool, error)
}

// Evaluator runs a package's detection rules through a Backend.
type Evaluator struct {
	Backend Backend
}

// EvaluatePackage reports whether ALL rules report Present == true. Zero
// rules returns (false, nil) so the caller knows the package has no
// detection (and should re-install every time, with a diagnostic).
func (e *Evaluator) EvaluatePackage(ctx context.Context, rules []swspec.DetectionRule) (bool, error) {
	if len(rules) == 0 {
		return false, nil
	}
	for i, r := range rules {
		ok, err := e.evaluateRule(ctx, r)
		if err != nil {
			return false, fmt.Errorf("rule %d (%s): %w", i, r.Type, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (e *Evaluator) evaluateRule(ctx context.Context, r swspec.DetectionRule) (bool, error) {
	switch r.Type {
	case "file":
		return e.evaluateFile(r)
	case "registry":
		return e.evaluateRegistry(r)
	case "msi":
		return e.Backend.MSIProductInstalled(r.MSIProductCode)
	case "script":
		return evaluateScript(ctx, r)
	default:
		return false, fmt.Errorf("unknown rule type %q", r.Type)
	}
}

func (e *Evaluator) evaluateFile(r swspec.DetectionRule) (bool, error) {
	// Expand %ProgramFiles%, %LOCALAPPDATA%, $HOME, etc once at the
	// top so the three host APIs below (Stat, FileVersion, SHA-256)
	// all see the same resolved path. Doing it here instead of inside
	// each Backend means the backends stay narrowly host-API specific
	// and the expansion semantics are testable through the fake
	// backend with no Windows syscall needed.
	path := expandPath(r.FilePath)
	present, err := e.Backend.FileExists(path)
	if err != nil || !present {
		return false, err
	}
	if r.FileVersion != "" {
		v, err := e.Backend.FileVersion(path)
		if err != nil {
			return false, err
		}
		if v != r.FileVersion {
			return false, nil
		}
	}
	if r.FileSHA256 != "" {
		got, err := sha256File(path)
		if err != nil {
			return false, err
		}
		if got != r.FileSHA256 {
			return false, nil
		}
	}
	return true, nil
}

func (e *Evaluator) evaluateRegistry(r swspec.DetectionRule) (bool, error) {
	present, value, err := e.Backend.RegistryValuePresent(r.RegistryHive, r.RegistryKey, r.RegistryValue)
	if err != nil || !present {
		return false, err
	}
	if r.RegistryEquals != "" && r.RegistryEquals != value {
		return false, nil
	}
	return true, nil
}

func evaluateScript(ctx context.Context, r swspec.DetectionRule) (bool, error) {
	var cmd *exec.Cmd
	switch r.ScriptShell {
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", r.ScriptBody)
	case "powershell":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass", "-Command", r.ScriptBody)
	default:
		return false, fmt.Errorf("script shell %q not supported", r.ScriptShell)
	}
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// A non-zero exit is the documented "not detected" signal — not an error.
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

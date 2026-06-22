//go:build windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// readSystemUUIDPlatform on Windows reads the SMBIOS UUID without
// requiring an external dependency. We try, in order:
//
//  1. PowerShell CIM query against Win32_ComputerSystemProduct -- the
//     modern, supported path and the one Microsoft documents.
//  2. wmic csproduct get UUID -- the legacy path. Still available on
//     all in-support Windows releases at the time of writing, but
//     deprecated since Windows 10 21H1.
//
// Returns the normalised UUID string (no whitespace, lower-case) or
// "" if both methods fail. Callers should treat "" as a hard error in
// production: the server identifies machines by SMBIOS UUID and an
// empty UUID silently breaks inventory and bulk-job claims.
func readSystemUUIDPlatform() string {
	if u := readUUIDViaPowerShell(); u != "" {
		return u
	}
	if u := readUUIDViaWMIC(); u != "" {
		return u
	}
	return ""
}

func readUUIDViaPowerShell() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command",
		"(Get-CimInstance -ClassName Win32_ComputerSystemProduct).UUID",
	).Output()
	if err != nil {
		return ""
	}
	return normaliseUUID(string(out))
}

func readUUIDViaWMIC() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wmic", "csproduct", "get", "UUID").Output()
	if err != nil {
		return ""
	}
	// wmic emits a two-line header plus the value. Pick the first
	// line that looks like a UUID.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "UUID") {
			continue
		}
		if u := normaliseUUID(line); u != "" {
			return u
		}
	}
	return ""
}

// normaliseUUID strips whitespace and lower-cases. A real SMBIOS UUID
// is 36 chars with hyphens (8-4-4-4-12); we accept that shape only,
// returning "" otherwise so the caller can fall through to the next
// strategy.
func normaliseUUID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if len(s) != 36 {
		return ""
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ""
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return ""
			}
		}
	}
	return s
}

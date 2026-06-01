//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// currentDomain reports the AD domain the machine is currently joined to and
// whether it is domain-joined at all (Win32_ComputerSystem.PartOfDomain).
func currentDomain() (domain string, joined bool) {
	const script = `$cs = Get-CimInstance Win32_ComputerSystem; "$($cs.PartOfDomain)|$($cs.Domain)"`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script).Output()
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) != 2 {
		return "", false
	}
	joined = strings.EqualFold(strings.TrimSpace(parts[0]), "True")
	return strings.TrimSpace(parts[1]), joined
}

// joinDomain joins the machine to domain with the given credentials, placing
// the computer object at ou when non-empty. The credentials are passed via
// environment variables (NOT the command line), so they never appear in the
// process table. Does not reboot; the caller schedules that.
func joinDomain(domain, ou, user, password string) error {
	const script = `$ErrorActionPreference = 'Stop'
$sec  = ConvertTo-SecureString $env:ADJOIN_PW -AsPlainText -Force
$cred = New-Object System.Management.Automation.PSCredential($env:ADJOIN_USER, $sec)
$p = @{ DomainName = $env:ADJOIN_DOMAIN; Credential = $cred; Force = $true }
if ($env:ADJOIN_OU) { $p['OUPath'] = $env:ADJOIN_OU }
Add-Computer @p`
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(),
		"ADJOIN_DOMAIN="+domain,
		"ADJOIN_OU="+ou,
		"ADJOIN_USER="+user,
		"ADJOIN_PW="+password,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Add-Computer failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

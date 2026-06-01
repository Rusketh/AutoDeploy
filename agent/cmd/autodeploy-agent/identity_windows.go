//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// collectObservedIdentity returns the machine's current Windows computer name
// and its Active Directory distinguished name. The DN is read via the built-in
// ADSI searcher (no RSAT required); it is empty on a workgroup machine or when
// the directory can't be reached. Best-effort: any failure leaves that field
// empty rather than blocking the poll.
func collectObservedIdentity() (name, dn string) {
	name, _ = os.Hostname()
	const script = `$ErrorActionPreference='SilentlyContinue'
$s=[ADSISearcher]"(&(objectCategory=computer)(sAMAccountName=$($env:COMPUTERNAME)$))"
$r=$s.FindOne()
if($r){ $r.Properties['distinguishedname'][0] }`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).Output()
	if err == nil {
		dn = strings.TrimSpace(string(out))
	}
	return name, dn
}

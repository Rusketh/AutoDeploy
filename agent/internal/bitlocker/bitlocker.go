// Package bitlocker drives the agent's BitLocker operations. On Windows
// hosts the Enable function calls Enable-BitLocker via PowerShell to
// turn on TPM+PIN encryption on C: and returns the generated recovery
// key. On non-Windows hosts (dev or CI) it returns ErrUnsupported so the
// caller can log and skip — never silently pretend encryption happened.
//
// The PIN and the recovery key are secrets. They appear in the
// PowerShell input we feed manage-bde and in its output respectively;
// neither value is written to any log. Recovery keys returned by Enable
// are escrowed to the server via the agent's BitLocker endpoint.
package bitlocker

import (
	"context"
	"errors"
)

// ErrUnsupported is returned on non-Windows hosts.
var ErrUnsupported = errors.New("BitLocker unsupported on this host")

// Driver is the type the agent calls. The Windows build (build-tag
// windows) provides a real implementation; other builds return
// ErrUnsupported.
type Driver struct {
	// Drive defaults to "C:" when empty.
	Drive string
}

func (d *Driver) drive() string {
	if d.Drive == "" {
		return "C:"
	}
	return d.Drive
}

// Enable turns on BitLocker on the target drive with a TPM+PIN
// protector and returns the generated 48-digit recovery key. The PIN is
// passed via PowerShell standard input — it is NOT placed on the
// process command line where another tool could observe it.
func (d *Driver) Enable(ctx context.Context, pin string) (string, error) {
	return driverEnable(ctx, d.drive(), pin)
}

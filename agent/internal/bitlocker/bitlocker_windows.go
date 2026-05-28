//go:build windows

package bitlocker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func driverEnable(ctx context.Context, drive, pin string) (string, error) {
	// We feed the PIN via stdin so it does not appear on the command
	// line. The PowerShell body reads stdin into $p, enables
	// BitLocker with TPM+PIN, then prints the freshly-generated
	// recovery password (and nothing else) so the caller can capture it.
	scriptBody := fmt.Sprintf(`$p = $input | Out-String
$p = $p.Trim() | ConvertTo-SecureString -AsPlainText -Force
Enable-BitLocker -MountPoint '%s' -EncryptionMethod Aes256 -UsedSpaceOnly `+
		`-TpmAndPinProtector -Pin $p -SkipHardwareTest | Out-Null
$key = (Get-BitLockerVolume -MountPoint '%s').KeyProtector |
       Where-Object { $_.KeyProtectorType -eq 'RecoveryPassword' } |
       Select-Object -ExpandProperty RecoveryPassword -First 1
Write-Output $key`, escape(drive), escape(drive))

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", scriptBody)
	cmd.Stdin = strings.NewReader(pin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{} // discard stderr; do not log it (may contain key material)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Enable-BitLocker: %w", err)
	}
	key := strings.TrimSpace(out.String())
	if key == "" {
		return "", fmt.Errorf("BitLocker recovery key not returned")
	}
	return key, nil
}

func escape(s string) string {
	// Single-quoted PowerShell strings need single-quote doubling.
	return strings.ReplaceAll(s, `'`, `''`)
}

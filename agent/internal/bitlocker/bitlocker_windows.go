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
	// When the PIN is complex (letters/symbols/spaces) we first raise the
	// "Allow enhanced PINs for startup" policy, without which BitLocker
	// rejects any non-numeric PIN; for a plain numeric PIN this is empty.
	scriptBody := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
%sClear-Tpm -ErrorAction SilentlyContinue | Out-Null
Initialize-Tpm -ErrorAction SilentlyContinue | Out-Null
$p = $input | Out-String
$p = $p.Trim() | ConvertTo-SecureString -AsPlainText -Force
Enable-BitLocker -MountPoint '%s' -EncryptionMethod Aes256 -UsedSpaceOnly `+
		`-TpmAndPinProtector -Pin $p -SkipHardwareTest | Out-Null
Add-BitLockerKeyProtector -MountPoint '%s' -RecoveryPasswordProtector | Out-Null
$key = (Get-BitLockerVolume -MountPoint '%s').KeyProtector |
       Where-Object { $_.KeyProtectorType -eq 'RecoveryPassword' } |
       Select-Object -ExpandProperty RecoveryPassword -First 1
Write-Output $key`, enhancedPINPolicyScript(pin), escape(drive), escape(drive), escape(drive))

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", scriptBody)
	cmd.Stdin = strings.NewReader(pin)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("Enable-BitLocker: %w", err)
		}
		return "", fmt.Errorf("Enable-BitLocker: %s", msg)
	}
	key := strings.TrimSpace(out.String())
	if key == "" {
		return "", fmt.Errorf("BitLocker recovery key not returned")
	}
	return key, nil
}

// driverState reports protection status and whether a TPM+PIN protector
// exists. Output is "<protection>|<hasTpmPin>" where protection is the
// integer ProtectionStatus (1 = On) and hasTpmPin is 0/1.
func driverState(ctx context.Context, drive string) (State, error) {
	scriptBody := fmt.Sprintf(`$v = Get-BitLockerVolume -MountPoint '%s' -ErrorAction Stop
$pin = @($v.KeyProtector | Where-Object { $_.KeyProtectorType -eq 'TpmPin' }).Count
Write-Output ("{0}|{1}" -f [int]$v.ProtectionStatus, [int]($pin -gt 0))`, escape(drive))
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", scriptBody)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return State{}, fmt.Errorf("Get-BitLockerVolume: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(out.String()), "|")
	if len(fields) != 2 {
		return State{}, fmt.Errorf("unexpected BitLocker state output %q", out.String())
	}
	return State{
		Protected: strings.TrimSpace(fields[0]) == "1",
		HasTPMPIN: strings.TrimSpace(fields[1]) == "1",
	}, nil
}

// driverChangePIN swaps the TPM+PIN protector for one with the new PIN. It
// removes existing TpmPin protectors and adds a fresh one; the
// recovery-password protector (and its GPO/AD backup) is never touched, so
// a site that backs recovery keys up to AD keeps doing so unaffected. The
// recovery password also keeps the volume protected during the swap.
func driverChangePIN(ctx context.Context, drive, pin string) error {
	// As in driverEnable, raise the enhanced-PIN policy first when the new
	// PIN is complex so Add-BitLockerKeyProtector accepts it; empty for a
	// plain numeric PIN.
	scriptBody := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
%s$p = $input | Out-String
$p = $p.Trim() | ConvertTo-SecureString -AsPlainText -Force
$v = Get-BitLockerVolume -MountPoint '%s'
foreach ($k in @($v.KeyProtector | Where-Object { $_.KeyProtectorType -eq 'TpmPin' })) {
    Remove-BitLockerKeyProtector -MountPoint '%s' -KeyProtectorId $k.KeyProtectorId | Out-Null
}
Add-BitLockerKeyProtector -MountPoint '%s' -TpmAndPinProtector -Pin $p | Out-Null`,
		enhancedPINPolicyScript(pin), escape(drive), escape(drive), escape(drive))
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", scriptBody)
	cmd.Stdin = strings.NewReader(pin)
	var changePINStderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &changePINStderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(changePINStderr.String())
		if msg == "" {
			return fmt.Errorf("change BitLocker PIN: %w", err)
		}
		return fmt.Errorf("change BitLocker PIN: %s", msg)
	}
	return nil
}

// driverDisable turns BitLocker off and decrypts the drive.
func driverDisable(ctx context.Context, drive string) error {
	scriptBody := fmt.Sprintf(
		`Disable-BitLocker -MountPoint '%s' -ErrorAction Stop | Out-Null`, escape(drive))
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", scriptBody)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Disable-BitLocker: %w", err)
	}
	return nil
}

func escape(s string) string {
	// Single-quoted PowerShell strings need single-quote doubling.
	return strings.ReplaceAll(s, `'`, `''`)
}

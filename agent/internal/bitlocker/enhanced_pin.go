package bitlocker

import "strings"

// enhancedPIN reports whether pin needs BitLocker's "enhanced PIN" policy to
// be accepted as a pre-boot startup PIN. By default BitLocker only allows the
// digits 0-9; a PIN containing any letter, symbol or space is rejected when
// the TPM+PIN protector is added with "Your PIN can only contain numbers from
// 0 to 9" (HRESULT 0x803100CC) unless the enhanced-PIN policy is on first.
//
// Surrounding whitespace is trimmed before the check, mirroring the .Trim()
// the PowerShell driver applies to the PIN it reads from stdin, so a numeric
// PIN with stray padding does not needlessly flip system policy.
func enhancedPIN(pin string) bool {
	for _, r := range strings.TrimSpace(pin) {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

// enhancedPINPolicyScript returns a PowerShell snippet that turns on the local
// "Allow enhanced PINs for startup" policy, or "" when pin is plain numeric
// and no policy change is warranted. It writes UseEnhancedPin=1 to the same
// HKLM key Group Policy uses; BitLocker reads that value directly when a
// protector is added, so it takes effect in the same elevated session with no
// gpupdate. The snippet is idempotent, creates the key if absent, and never
// lowers the policy — it only raises it just-in-time for a complex PIN. The
// PIN itself is never interpolated into the script (it travels via stdin).
func enhancedPINPolicyScript(pin string) string {
	if !enhancedPIN(pin) {
		return ""
	}
	const key = `HKLM:\SOFTWARE\Policies\Microsoft\FVE`
	return "New-Item -Path '" + key + "' -Force | Out-Null\n" +
		"New-ItemProperty -Path '" + key + "' -Name 'UseEnhancedPin' -Value 1 -PropertyType DWord -Force | Out-Null\n"
}

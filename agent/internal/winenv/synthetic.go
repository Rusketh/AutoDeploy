// Package winenv expands Windows %VAR% environment references used in the
// agent's detection-rule and install-step paths.
//
// Most variables (%ProgramFiles%, %SystemRoot%, %ProgramData%, %LOCALAPPDATA%,
// …) resolve straight from the process environment via the same
// ExpandEnvironmentStrings the shell uses. The bitness-suffixed "synthetic"
// variables, though -- %ProgramFiles(x86)%, %ProgramW6432%,
// %CommonProgramFiles(x86)% and %CommonProgramW6432% -- are injected into a
// process's environment block by the loader, NOT stored in the registry's
// Environment key, and they are frequently ABSENT from the stripped-down
// environment a non-interactive Windows SERVICE runs under. The agent runs as
// a service, so a detection rule like
//
//	%ProgramFiles(x86)%\HUE Intuition\Hue Camera Manager.exe
//
// would otherwise be left literal (the OS can't expand a variable it doesn't
// have), os.Stat would never find it, and the package would re-install on
// every deploy. We fix this by resolving those four variables from their
// canonical registry source (HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion)
// as a fallback. See Expand (platform-specific).
package winenv

import "regexp"

// syntheticVars maps each synthetic %VAR% (matched case-insensitively, as
// Windows environment variables are) to the value name under
// HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion that holds its path.
var syntheticVars = []struct {
	re     *regexp.Regexp
	regVal string
}{
	{regexp.MustCompile(`(?i)%ProgramFiles\(x86\)%`), "ProgramFilesDir (x86)"},
	{regexp.MustCompile(`(?i)%ProgramW6432%`), "ProgramW6432Dir"},
	{regexp.MustCompile(`(?i)%CommonProgramFiles\(x86\)%`), "CommonFilesDir (x86)"},
	{regexp.MustCompile(`(?i)%CommonProgramW6432%`), "CommonW6432Dir"},
}

// patchSynthetic replaces any synthetic %VAR% tokens that survived OS
// expansion (i.e. weren't present in the process environment) with the value
// lookup returns for that variable's registry value name. lookup reports
// ok=false when the value is missing or unreadable, in which case the token is
// left untouched so the path surfaces a real "not found" rather than a
// corrupted absolute path. Pure and platform-independent so it can be
// unit-tested without a real registry.
func patchSynthetic(s string, lookup func(regVal string) (string, bool)) string {
	if s == "" {
		return s
	}
	for _, sv := range syntheticVars {
		if !sv.re.MatchString(s) {
			continue
		}
		val, ok := lookup(sv.regVal)
		if !ok || val == "" {
			continue
		}
		s = sv.re.ReplaceAllLiteralString(s, val)
	}
	return s
}

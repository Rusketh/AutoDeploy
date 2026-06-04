//go:build windows

package winenv

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Expand expands Windows %VAR% references in s against the process
// environment (the same ExpandEnvironmentStrings the shell uses), then
// resolves any synthetic Program Files variables that weren't present from
// the registry as a fallback (see the package doc for why a service's
// environment can be missing %ProgramFiles(x86)% et al). On any expansion
// failure the input is returned unchanged so a bad value surfaces as a real
// "not found" at run time rather than a confusing error.
func Expand(s string) string {
	if s == "" {
		return s
	}
	out, err := registry.ExpandString(s)
	if err != nil {
		out = s
	}
	// Only touch the registry if a %VAR% token survived expansion -- the
	// common case (everything resolved from the environment) pays nothing.
	if !strings.Contains(out, "%") {
		return out
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return out
	}
	defer k.Close()
	return patchSynthetic(out, func(name string) (string, bool) {
		v, _, err := k.GetStringValue(name)
		if err != nil {
			return "", false
		}
		return v, true
	})
}

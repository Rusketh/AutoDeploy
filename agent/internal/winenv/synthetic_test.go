package winenv

import "testing"

// patchSynthetic resolves the bitness-suffixed Program Files variables that an
// OS expansion leaves behind (because a service's environment lacks them) from
// their registry values, case-insensitively, while leaving variables it
// doesn't know about for the OS to handle.
func TestPatchSyntheticReplacesMissingVars(t *testing.T) {
	lookup := func(name string) (string, bool) {
		m := map[string]string{
			"ProgramFilesDir (x86)": `C:\Program Files (x86)`,
			"ProgramW6432Dir":       `C:\Program Files`,
			"CommonFilesDir (x86)":  `C:\Program Files (x86)\Common Files`,
			"CommonW6432Dir":        `C:\Program Files\Common Files`,
		}
		v, ok := m[name]
		return v, ok
	}
	cases := []struct{ in, want string }{
		{`%ProgramFiles(x86)%\HUE Intuition\Hue Camera Manager.exe`,
			`C:\Program Files (x86)\HUE Intuition\Hue Camera Manager.exe`},
		// Case-insensitive, as Windows environment variables are.
		{`%programfiles(x86)%\x.exe`, `C:\Program Files (x86)\x.exe`},
		{`%ProgramW6432%\Acme\a.exe`, `C:\Program Files\Acme\a.exe`},
		{`%CommonProgramFiles(x86)%\m.dll`, `C:\Program Files (x86)\Common Files\m.dll`},
		{`%CommonProgramW6432%\m.dll`, `C:\Program Files\Common Files\m.dll`},
		// %ProgramFiles% is NOT synthetic (it's always in the environment), so
		// it is left for the OS expander -- the (x86) regex must not match it.
		{`%ProgramFiles%\Acme\a.exe`, `%ProgramFiles%\Acme\a.exe`},
		{"", ""},
	}
	for _, c := range cases {
		if got := patchSynthetic(c.in, lookup); got != c.want {
			t.Errorf("patchSynthetic(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// When the registry value is missing the token is left as-is rather than
// blanked, so the path surfaces a real "not found" instead of becoming a
// corrupted absolute path like "\HUE Intuition\...".
func TestPatchSyntheticLeavesTokenWhenLookupMisses(t *testing.T) {
	miss := func(string) (string, bool) { return "", false }
	in := `%ProgramFiles(x86)%\x.exe`
	if got := patchSynthetic(in, miss); got != in {
		t.Errorf("expected token preserved on lookup miss, got %q", got)
	}
}

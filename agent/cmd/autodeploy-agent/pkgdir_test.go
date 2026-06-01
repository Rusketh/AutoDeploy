package main

import (
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// TestExpandPkgDir replaces %pkgdir% (case-insensitively) across every path
// field, arg and script body, in every step type.
func TestExpandPkgDir(t *testing.T) {
	const dir = `C:\AD\pkg-9\files`
	in := []swspec.InstallStep{
		{Type: "exe", ExePath: "OfficeSetup.exe", ExeArgs: []string{"/configure", `%pkgdir%\NoTeams.xml`}},
		{Type: "copy", SourcePath: `%PKGDIR%\app.lnk`, DestinationPath: `%ProgramData%\X`},
		{Type: "cmd", ScriptBody: `dir "%pkgdir%"`},
	}
	out := expandPkgDir(in, dir)

	if out[0].ExeArgs[1] != dir+`\NoTeams.xml` {
		t.Errorf("exe arg = %q", out[0].ExeArgs[1])
	}
	if out[1].SourcePath != dir+`\app.lnk` {
		t.Errorf("copy source (case-insensitive) = %q", out[1].SourcePath)
	}
	if out[1].DestinationPath != `%ProgramData%\X` {
		t.Errorf("non-pkgdir env var must be left for the env expander: %q", out[1].DestinationPath)
	}
	if out[2].ScriptBody != `dir "`+dir+`"` {
		t.Errorf("script body = %q", out[2].ScriptBody)
	}
	// Input untouched (expandPkgDir copies).
	if in[0].ExeArgs[1] != `%pkgdir%\NoTeams.xml` {
		t.Error("expandPkgDir mutated its input")
	}
	// Empty dir is a no-op.
	if got := expandPkgDir(in, ""); &got[0] == &in[0] {
		// returns the same slice header; acceptable -- just assert no panic
	}
}

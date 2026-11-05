//go:build windows

package main

import (
	"log/slog"
	"os/exec"
	"strings"
)

// secureWorkDir restricts the agent's work folder to SYSTEM and
// Administrators. It is created under C:\ProgramData, whose default ACL lets
// any local user create files in subfolders -- enough to plant a DLL or swap
// a file next to an installer the agent is about to run as SYSTEM. Inherited
// entries are dropped and only the two trusted principals are granted; a Tool
// Kit later grants its Run As account read/execute on its own job folder.
// Best effort: a failure is logged, never fatal.
//
// Only when the agent runs as SYSTEM (the service): an operator running it by
// hand, unelevated, must not lock themselves out of their own folder.
func secureWorkDir(log *slog.Logger, dir string) {
	if !runningAsSystem() {
		return
	}
	out, err := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F", "/T", "/C", "/Q").CombinedOutput()
	if err != nil {
		log.Warn("workdir.secure", slog.String("target", dir), slog.String("error", err.Error()),
			slog.String("output", strings.TrimSpace(string(out))))
	}
}

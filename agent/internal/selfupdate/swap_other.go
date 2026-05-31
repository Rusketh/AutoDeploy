//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Swap does the in-place upgrade on POSIX hosts. The dev / CI agent
// runs on Linux during testing; production Windows targets use
// swap_windows.go. The mechanic is the same: write a sh updater that
// waits for the agent process to exit, swaps the binary, relaunches.
// serviceName is ignored off Windows (there's no SCM); the signature
// matches swap_windows.go so callers don't need build tags.
func Swap(newExe string, relaunchArgs []string, serviceName string) error {
	cur, err := CurrentExe()
	if err != nil {
		return err
	}
	pid := os.Getpid()
	updater := cur + ".update.sh"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AutoDeploy agent self-update\n")
	b.WriteString("set -eu\n")
	// Wait for the running agent to exit, up to 30 seconds.
	fmt.Fprintf(&b, "for i in $(seq 1 30); do\n")
	fmt.Fprintf(&b, "  if ! kill -0 %d 2>/dev/null; then break; fi\n", pid)
	b.WriteString("  sleep 1\n")
	b.WriteString("done\n")
	fmt.Fprintf(&b, "if kill -0 %d 2>/dev/null; then\n", pid)
	b.WriteString("  echo 'autodeploy-agent did not exit in 30s; aborting update' >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	// Move new over old, keeping a .bak for one-step rollback.
	fmt.Fprintf(&b, "mv -f %s %s.bak\n", shQuote(cur), shQuote(cur))
	fmt.Fprintf(&b, "if ! mv -f %s %s; then\n", shQuote(newExe), shQuote(cur))
	fmt.Fprintf(&b, "  mv -f %s.bak %s\n", shQuote(cur), shQuote(cur))
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	fmt.Fprintf(&b, "chmod +x %s\n", shQuote(cur))
	// Relaunch with original command-line.
	b.WriteString("(")
	b.WriteString(shQuote(cur))
	for _, a := range relaunchArgs[1:] {
		b.WriteString(" ")
		b.WriteString(shQuote(a))
	}
	b.WriteString(") &\n")
	// Self-delete after a short settle.
	b.WriteString("sleep 5\n")
	fmt.Fprintf(&b, "rm -f %s.bak %s\n", shQuote(cur), shQuote(updater))
	if err := os.WriteFile(updater, []byte(b.String()), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", updater)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, ` "'\$&|;<>(){}*?#~!`+"\t\n") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Cleanup removes leftover .bak / .new / .update.sh files alongside
// the running executable. Called at agent startup.
func Cleanup() {
	cur, err := CurrentExe()
	if err != nil {
		return
	}
	for _, suf := range []string{".bak", ".new", ".new.tmp", ".update.sh"} {
		_ = os.Remove(cur + suf)
	}
	matches, _ := filepath.Glob(cur + ".update*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

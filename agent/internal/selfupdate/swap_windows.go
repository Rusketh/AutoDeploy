//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Swap performs the in-place upgrade on Windows. newExe must already
// be downloaded and SHA-256-verified by the caller. relaunchArgs are
// the command-line args that the freshly-installed agent should be
// started with (typically os.Args from the running process). The
// function writes a .cmd updater, spawns it detached, and returns;
// the caller is expected to exit immediately afterward.
func Swap(newExe string, relaunchArgs []string) error {
	cur, err := CurrentExe()
	if err != nil {
		return err
	}
	if cur == "" {
		return fmt.Errorf("self-update: cannot resolve current executable path")
	}
	pid := os.Getpid()
	updater := cur + ".update.cmd"
	// Quote each argument for cmd.exe. The relaunch command line is
	// just `start "" "<exe>" <arg1> <arg2> ...` so cmd's quoting
	// rules apply: wrap each token containing whitespace in double
	// quotes, escape embedded double quotes with backslash. We're
	// conservative -- wrap every token unconditionally except the
	// exe itself (which we always wrap).
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("rem AutoDeploy agent self-update.\r\n")
	// Wait for the running agent to exit. Poll up to 30 seconds; if
	// it doesn't exit we abort the swap rather than risk corrupting
	// the running binary.
	fmt.Fprintf(&b, "set /a tries=0\r\n")
	fmt.Fprintf(&b, ":wait\r\n")
	fmt.Fprintf(&b, "tasklist /FI \"PID eq %d\" 2>NUL | find \" %d \" >NUL\r\n", pid, pid)
	fmt.Fprintf(&b, "if errorlevel 1 goto :swap\r\n")
	fmt.Fprintf(&b, "set /a tries+=1\r\n")
	fmt.Fprintf(&b, "if %%tries%% gtr 30 goto :abort\r\n")
	fmt.Fprintf(&b, "timeout /t 1 /nobreak >NUL\r\n")
	fmt.Fprintf(&b, "goto :wait\r\n")
	fmt.Fprintf(&b, ":swap\r\n")
	// Move new over old, keeping a .bak for one-step rollback.
	fmt.Fprintf(&b, "move /Y \"%s\" \"%s.bak\" >NUL\r\n", cur, cur)
	fmt.Fprintf(&b, "move /Y \"%s\" \"%s\" >NUL\r\n", newExe, cur)
	fmt.Fprintf(&b, "if errorlevel 1 (\r\n")
	fmt.Fprintf(&b, "  move /Y \"%s.bak\" \"%s\" >NUL\r\n", cur, cur)
	fmt.Fprintf(&b, "  goto :abort\r\n")
	fmt.Fprintf(&b, ")\r\n")
	// Relaunch with the same command-line.
	b.WriteString("start \"\" ")
	b.WriteString(quoteForCmd(cur))
	for _, a := range relaunchArgs[1:] { // skip argv[0]
		b.WriteString(" ")
		b.WriteString(quoteForCmd(a))
	}
	b.WriteString("\r\n")
	// Self-delete the updater script and the .bak after a short
	// settle time so a quick re-update doesn't overwrite the .bak
	// before the agent has stabilised.
	fmt.Fprintf(&b, "timeout /t 5 /nobreak >NUL\r\n")
	fmt.Fprintf(&b, "del \"%s.bak\" >NUL 2>&1\r\n", cur)
	fmt.Fprintf(&b, "del \"%%~f0\" >NUL 2>&1\r\n")
	b.WriteString("exit /b 0\r\n")
	b.WriteString(":abort\r\n")
	b.WriteString("exit /b 1\r\n")
	if err := os.WriteFile(updater, []byte(b.String()), 0o755); err != nil {
		return err
	}
	// Spawn detached so the parent (this agent process) can exit
	// without taking the updater down with it. cmd /c invokes the
	// script; start /B keeps it in the same console. We use
	// `cmd /c start "" /B cmd /c <script>` which double-detaches.
	cmd := exec.Command("cmd", "/C", "start", "", "/B", "cmd", "/C", updater)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Don't Wait -- the whole point is the updater outlives us.
	return nil
}

// quoteForCmd produces a token safe to pass to cmd.exe. We
// double-quote anything that isn't purely safe characters; embedded
// quotes get backslash-escaped per cmd's convention.
func quoteForCmd(s string) string {
	safe := s != "" && !strings.ContainsAny(s, " \t\"&|<>^()%!")
	if safe {
		return s
	}
	escaped := strings.ReplaceAll(s, `"`, `\"`)
	return `"` + escaped + `"`
}

// Cleanup removes the .bak and any stale .new / .tmp / .update.cmd
// files next to the current executable. Called at agent startup so a
// successful update leaves no debris.
func Cleanup() {
	cur, err := CurrentExe()
	if err != nil {
		return
	}
	for _, suf := range []string{".bak", ".new", ".new.tmp", ".update.cmd"} {
		_ = os.Remove(cur + suf)
	}
	// Older versions may have written the script under a slightly
	// different name; remove anything matching <exe>.update*.
	matches, _ := filepath.Glob(cur + ".update*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

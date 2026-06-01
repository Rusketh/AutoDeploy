package steps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestOSRunnerWorkDir verifies a Run'd process executes in OSRunner.WorkDir,
// so an installer's relative args resolve against the package work dir rather
// than the service's CWD. Uses a shell to create a file by relative name and
// asserts it lands in WorkDir.
func TestOSRunnerWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell; the behaviour under test is OS-independent")
	}
	dir := t.TempDir()
	r := &OSRunner{WorkDir: dir}
	code, err := r.Run(context.Background(), "sh", []string{"-c", "echo hi > marker.txt"}, "")
	if err != nil || code != 0 {
		t.Fatalf("run failed: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Errorf("expected marker.txt in WorkDir %s: %v", dir, err)
	}
}

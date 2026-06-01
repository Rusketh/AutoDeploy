//go:build linux

package api

import (
	"os"
	"os/exec"
	"syscall"
)

// serverUpdateHelperPath is where install-linux.sh drops the in-place
// upgrade script. Hard-coded so the portal endpoint never executes an
// arbitrary path -- if the operator moved the script, they can
// symlink /usr/local/sbin/autodeploy-update back to it.
func serverUpdateHelperPath() string { return "/usr/local/sbin/autodeploy-update" }

// execCommand is the test seam for exec.Command. Default is the real
// one; tests replace it to avoid actually spawning sudo.
var execCommand = exec.Command

// newDetachedSysProcAttr returns a SysProcAttr that puts the spawned
// process in its own session, so it survives the parent's exit. The
// update script's first action is to stop autodeploy.service, which
// kills the parent server process -- the child must outlive us.
func newDetachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// processIsRunning checks whether a process with the given PID is
// still alive. It sends signal 0 which doesn't affect the process
// but fails with ESRCH if it doesn't exist.
func processIsRunning(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

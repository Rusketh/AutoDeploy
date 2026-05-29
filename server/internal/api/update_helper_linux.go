//go:build linux

package api

import (
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

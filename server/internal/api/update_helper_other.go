//go:build !linux

package api

import (
	"os/exec"
	"syscall"
)

// serverUpdateHelperPath returns an empty / non-existent path on
// platforms that don't ship the in-place update helper. The
// /api/v1/server/update handler will return 503 when this file
// doesn't exist, which is the correct behaviour for unsupported
// platforms.
func serverUpdateHelperPath() string {
	// Windows uses a separate updater (autodeploy-update.ps1) wired
	// to the Windows Service Manager; that surface is out of scope
	// for this build. Return a path that won't os.Stat-succeed so
	// the handler reports the helper is not installed.
	return "C:\\Program Files\\AutoDeploy\\autodeploy-update.cmd"
}

var execCommand = exec.Command

func newDetachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

//go:build !linux

package portal

// updaterHelperPath returns the conventional non-Linux path. The
// portal page surfaces the value so the operator sees exactly which
// file the "Update" button would invoke; on platforms where the
// updater isn't shipped the os.Stat in the page handler will return
// not-exist and the button is hidden.
func updaterHelperPath() string {
	return "C:\\Program Files\\AutoDeploy\\autodeploy-update.cmd"
}

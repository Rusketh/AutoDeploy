//go:build linux

package portal

// updaterHelperPath mirrors the api package's
// serverUpdateHelperPath. Kept private to portal so the templates can
// render the path without the portal pulling in the api package
// (which would be a cycle through Repos types).
func updaterHelperPath() string { return "/usr/local/sbin/autodeploy-update" }

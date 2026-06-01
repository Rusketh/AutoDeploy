//go:build !windows

package main

import "os"

// collectObservedIdentity off Windows reports the hostname only; there is no
// AD distinguished name. The deployed agent only ever runs on Windows -- this
// keeps the resident loop building and testable on the dev host.
func collectObservedIdentity() (name, dn string) {
	name, _ = os.Hostname()
	return name, ""
}

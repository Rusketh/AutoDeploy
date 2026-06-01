//go:build !windows

package main

import "errors"

// currentDomain off Windows can't read AD membership; report "not joined" so
// the resident loop builds and tests on the dev host.
func currentDomain() (string, bool) { return "", false }

// joinDomain is Windows-only; the deployed agent only ever runs on Windows.
func joinDomain(domain, ou, user, password string) error {
	return errors.New("domain join is Windows-only")
}

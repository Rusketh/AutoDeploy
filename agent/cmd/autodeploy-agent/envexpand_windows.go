//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// expandEnv expands Windows environment-variable references (%VAR%) in a path
// using the live process environment, via the same Win32
// ExpandEnvironmentStrings the shell uses. Go's os.Expand only understands
// $VAR/${VAR}, so copy/unzip destinations and msi/exe paths that use %VAR%
// would otherwise be treated literally. On error the input is returned
// unchanged so a bad value surfaces as a real "not found" at run time.
func expandEnv(s string) string {
	if s == "" {
		return s
	}
	out, err := registry.ExpandString(s)
	if err != nil {
		return s
	}
	return out
}

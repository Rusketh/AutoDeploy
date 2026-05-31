//go:build !windows

package main

import (
	"context"
	"errors"
)

// errNoService is returned by the service shims on non-Windows builds.
var errNoService = errors.New("windows service control is not supported on this platform")

func isWindowsService() bool                     { return false }
func runService(func(ctx context.Context)) error { return errNoService }
func installService() error                      { return errNoService }
func uninstallService() error                    { return errNoService }

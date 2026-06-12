//go:build !windows

package main

// collectHardware is a no-op off Windows; the deployed agent only ever
// runs on Windows. Returns nil so the caller skips the hardware report.
func collectHardware() map[string]any { return nil }

// collectSMBIOSIdentity is likewise Windows-only (WMI); nil means the
// caller reports identity as UUID-only, exactly as before.
func collectSMBIOSIdentity() map[string]any { return nil }

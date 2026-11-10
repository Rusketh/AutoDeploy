package main

import "testing"

// Routine "already installed" lines are shipped only when a package's state
// changes, not on every poll.
func TestMarkPackageInstalledReportsChanges(t *testing.T) {
	const id = 987654
	steps := []struct {
		installed, changed bool
	}{
		{true, true},   // first check this run
		{true, false},  // unchanged poll: not logged
		{true, false},  // still unchanged
		{false, true},  // uninstalled
		{false, false}, // still missing
		{true, true},   // reinstalled
	}
	for i, s := range steps {
		if got := markPackageInstalled(id, s.installed); got != s.changed {
			t.Errorf("step %d: markPackageInstalled(%v) = %v, want %v", i, s.installed, got, s.changed)
		}
	}
}

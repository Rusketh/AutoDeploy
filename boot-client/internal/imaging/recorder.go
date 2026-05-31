package imaging

import (
	"context"
	"fmt"
	"strings"
)

// Recorder is a Runner that records every command instead of executing it.
// Used in tests to assert the imaging pipeline issues the right calls in the
// right order without touching real disks.
type Recorder struct {
	Calls []string
	// OutputResult is what Output returns (defaults to ""). Tests set it to
	// simulate command stdout, e.g. an efibootmgr entry listing.
	OutputResult string
}

// Exec implements Runner.
func (r *Recorder) Exec(_ context.Context, name string, args ...string) error {
	r.Calls = append(r.Calls, name+" "+strings.Join(args, " "))
	return nil
}

// Output implements Runner: records the call and returns OutputResult.
func (r *Recorder) Output(_ context.Context, name string, args ...string) (string, error) {
	r.Calls = append(r.Calls, name+" "+strings.Join(args, " "))
	return r.OutputResult, nil
}

// Has reports whether any recorded call contains the given substring.
func (r *Recorder) Has(sub string) bool {
	for _, c := range r.Calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// Dump prints the recorded calls — useful for failing-test diagnostics.
func (r *Recorder) Dump() string {
	return fmt.Sprintf("%d calls:\n  - %s", len(r.Calls), strings.Join(r.Calls, "\n  - "))
}

package main

import "testing"

// TestAdJoinStatusJoinFailedLayering checks the combining logic in adJoinStatus:
// off Windows collectADJoinStatus reports "" (nothing), so the last agent-driven
// join failure is what surfaces as join_failed, and clearing it goes back to "".
func TestAdJoinStatusJoinFailedLayering(t *testing.T) {
	t.Cleanup(func() { adJoinLastError = "" })

	// No failure recorded and not domain-joined (stub) → report nothing.
	adJoinLastError = ""
	if status, detail := adJoinStatus(); status != "" || detail != "" {
		t.Fatalf("clean state: got (%q,%q), want empty", status, detail)
	}

	// A recorded join failure surfaces as join_failed carrying the detail.
	adJoinLastError = "Add-Computer failed: access is denied"
	status, detail := adJoinStatus()
	if status != adStatusJoinFailed {
		t.Errorf("status = %q, want %q", status, adStatusJoinFailed)
	}
	if detail != adJoinLastError {
		t.Errorf("detail = %q, want %q", detail, adJoinLastError)
	}

	// Clearing the failure (successful join / confirmed membership) returns to
	// reporting nothing on the stub host.
	adJoinLastError = ""
	if status, detail := adJoinStatus(); status != "" || detail != "" {
		t.Errorf("after clear: got (%q,%q), want empty", status, detail)
	}
}

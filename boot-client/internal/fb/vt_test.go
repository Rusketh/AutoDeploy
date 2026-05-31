package fb

import "testing"

// acquireVT must never panic and must degrade gracefully when no console
// can be switched (the CI container has no usable VT). A nil guard's
// release() must also be safe -- Close() calls it on the fallback path.
func TestVTGuardGraceful(t *testing.T) {
	v := acquireVT() // nil or a real guard depending on environment
	// release must be safe whether or not a console was acquired.
	v.release()
	// And explicitly safe on a nil receiver.
	var nilGuard *vtGuard
	nilGuard.release()
}

func TestKDModeConstants(t *testing.T) {
	// Guard against a typo in the mode values: text=0, graphics=1.
	if kdModeText != 0 || kdModeGraphics != 1 {
		t.Errorf("KD mode constants wrong: text=%d graphics=%d", kdModeText, kdModeGraphics)
	}
}

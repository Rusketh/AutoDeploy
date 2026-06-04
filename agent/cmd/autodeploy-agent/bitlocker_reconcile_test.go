package main

import (
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/bitlocker"
)

// decideBitLockerAction is the heart of the resident reconcile: it must enable
// on a freshly-PINned machine, change the PIN in place when the operator
// updates it, decrypt when the PIN is cleared, and otherwise do nothing
// (notably: never swap the PIN without a baseline to compare against).
func TestDecideBitLockerAction(t *testing.T) {
	st := func(prot, pin bool) bitlocker.State {
		return bitlocker.State{Protected: prot, HasTPMPIN: pin}
	}
	cases := []struct {
		name        string
		pinSet      bool
		version     int64
		st          bitlocker.State
		lastApplied int64
		want        blAction
	}{
		{"cleared + encrypted -> decrypt", false, 0, st(true, true), 100, blDecrypt},
		{"cleared + not encrypted -> none", false, 0, st(false, false), 0, blNone},
		{"set + not encrypted -> enable", true, 100, st(false, false), 0, blEnable},
		{"set + encrypted, no PIN protector -> change (adds one)", true, 100, st(true, false), 0, blChangePIN},
		{"set + encrypted + TPM-PIN, version bumped -> change", true, 200, st(true, true), 100, blChangePIN},
		{"set + encrypted + TPM-PIN, version unchanged -> none", true, 100, st(true, true), 100, blNone},
		{"set + encrypted + TPM-PIN, no baseline -> none (no needless swap)", true, 100, st(true, true), 0, blNone},
		{"set + encrypted + TPM-PIN, version older than baseline -> none", true, 50, st(true, true), 100, blNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideBitLockerAction(c.pinSet, c.version, c.st, c.lastApplied); got != c.want {
				t.Errorf("decideBitLockerAction(pinSet=%v, version=%d, %+v, lastApplied=%d) = %d, want %d",
					c.pinSet, c.version, c.st, c.lastApplied, got, c.want)
			}
		})
	}
}

// The applied-version baseline round-trips through the work-dir state file,
// and a missing file reads as 0 (no baseline).
func TestAppliedBLVersionRoundTrip(t *testing.T) {
	f := agentFlags{workDir: t.TempDir()}
	if v := readAppliedBLVersion(f); v != 0 {
		t.Errorf("fresh state = %d, want 0", v)
	}
	writeAppliedBLVersion(f, 1717500000)
	if v := readAppliedBLVersion(f); v != 1717500000 {
		t.Errorf("round-trip = %d, want 1717500000", v)
	}
	writeAppliedBLVersion(f, 0) // decrypt records 0
	if v := readAppliedBLVersion(f); v != 0 {
		t.Errorf("cleared = %d, want 0", v)
	}
}

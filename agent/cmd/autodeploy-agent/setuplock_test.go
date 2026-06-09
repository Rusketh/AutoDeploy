package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLockMarkerLifecycle(t *testing.T) {
	lockStateDir = t.TempDir()
	if lockMarkerPresent() {
		t.Fatal("marker present before arming")
	}
	if err := armLockMarker(); err != nil {
		t.Fatal(err)
	}
	if !lockMarkerPresent() {
		t.Fatal("marker missing after arm")
	}
	clearLockMarker()
	if lockMarkerPresent() {
		t.Fatal("marker present after clear")
	}
}

func TestWriteLockStatus(t *testing.T) {
	lockStateDir = t.TempDir()
	writeLockStatus("installing", "Installing Microsoft Office", 3, 9)
	b, err := os.ReadFile(lockPath("status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st lockStatus
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.Phase != "installing" || st.Activity != "Installing Microsoft Office" {
		t.Errorf("status fields = %+v", st)
	}
	if st.Done != 3 || st.Total != 9 || st.Percent != 33 {
		t.Errorf("progress = %d/%d (%d%%), want 3/9 (33%%)", st.Done, st.Total, st.Percent)
	}
	if st.UpdatedAt == "" {
		t.Error("updated_at not set")
	}
	// A zero total renders an indeterminate bar.
	writeLockStatus("agent-online", "Preparing", 0, 0)
	b, _ = os.ReadFile(lockPath("status.json"))
	_ = json.Unmarshal(b, &st)
	if st.Percent != -1 {
		t.Errorf("percent = %d, want -1 (indeterminate)", st.Percent)
	}
}

func TestWatchLockPINRequests(t *testing.T) {
	lockStateDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	unlocked := make(chan struct{}, 4)
	// Validator accepts only "1234" (stands in for the rate-limited server gate).
	go watchLockPINRequests(ctx, log,
		func(_ context.Context, pin string) bool { return pin == "1234" },
		func() { unlocked <- struct{}{} })

	respond := func(pin string) string {
		t.Helper()
		if err := writeFileAtomic(lockPath("pin-request"), []byte(pin)); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(lockPath("pin-response"))
			if err == nil {
				_ = os.Remove(lockPath("pin-response"))
				return string(b)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no pin-response for %q within deadline", pin)
		return ""
	}

	// A valid PIN: "allow" + onUnlock fires.
	if got := respond("1234"); got != "allow" {
		t.Errorf("pin 1234 -> %q, want allow", got)
	}
	select {
	case <-unlocked:
	case <-time.After(2 * time.Second):
		t.Error("onUnlock not called for a valid PIN")
	}

	// A wrong PIN: "deny" + onUnlock does NOT fire.
	if got := respond("0000"); got != "deny" {
		t.Errorf("pin 0000 -> %q, want deny", got)
	}
	select {
	case <-unlocked:
		t.Error("onUnlock called for an invalid PIN")
	case <-time.After(300 * time.Millisecond):
	}
}

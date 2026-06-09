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
	// Validator accepts only "1234" (stands in for the rate-limited server gate).
	go watchLockPINRequests(ctx, log, func(_ context.Context, pin string) bool {
		return pin == "1234"
	})

	check := func(pin, want string) {
		t.Helper()
		if err := writeFileAtomic(lockPath("pin-request"), []byte(pin)); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(lockPath("pin-response"))
			if err == nil {
				if string(b) != want {
					t.Errorf("pin %q -> %q, want %q", pin, string(b), want)
				}
				_ = os.Remove(lockPath("pin-response"))
				if _, serr := os.Stat(lockPath("pin-request")); !os.IsNotExist(serr) {
					t.Errorf("pin-request not consumed for %q", pin)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no pin-response for %q within deadline", pin)
	}
	check("1234", "allow")
	check("0000", "deny")
}

package model

import (
	"testing"
	"time"
)

// Notifications store time.Now() as its String() form, monotonic reading and
// all; those rows must read back as their real time, not the zero time.
func TestParseDBTimeMonotonicSuffix(t *testing.T) {
	got := parseDBTime("2026-10-29 11:22:17.353404429 +0000 UTC m=+361.606632797")
	want := time.Date(2026, 10, 29, 11, 22, 17, 353404429, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parseDBTime = %v, want %v", got, want)
	}
	if parseDBTime("2026-10-29 11:22:17").IsZero() {
		t.Fatal("plain SQLite timestamp no longer parses")
	}
}

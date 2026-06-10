package runtime

import (
	"testing"
	"time"
)

// TestDisplayLocation covers the default (UTC), a valid IANA override, and the
// safe fallback to UTC for an unloadable value. The getter reads only the
// cache, so it needs no database.
func TestDisplayLocation(t *testing.T) {
	s := &Settings{cache: map[string]string{}}

	// Unset -> UTC.
	if got := s.DisplayLocation(); got != time.UTC {
		t.Errorf("unset DisplayLocation = %v, want UTC", got)
	}
	if got := s.DisplayTimezone(); got != "" {
		t.Errorf("unset DisplayTimezone = %q, want empty", got)
	}

	// Valid zone resolves to that location.
	s.cache[keyDisplayTimezone] = "America/New_York"
	loc := s.DisplayLocation()
	if loc == nil || loc.String() != "America/New_York" {
		t.Fatalf("DisplayLocation = %v, want America/New_York", loc)
	}
	// Spot-check it actually applies an offset (EST is UTC-5 in January).
	ref := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if _, off := ref.In(loc).Zone(); off != -5*3600 {
		t.Errorf("Jan offset = %ds, want -18000 (EST)", off)
	}

	// Garbage zone falls back to UTC rather than panicking/erroring.
	s.cache[keyDisplayTimezone] = "Not/AZone"
	if got := s.DisplayLocation(); got != time.UTC {
		t.Errorf("bad zone DisplayLocation = %v, want UTC fallback", got)
	}
}

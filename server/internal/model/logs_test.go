package model

import (
	"context"
	"testing"
	"time"
)

// TestLogSearchScansVariedTimestampFormats guards the Logs page against the
// pure-Go SQLite driver handing back a raw timestamp string it could not
// auto-parse (which previously failed with "unsupported Scan ... string into
// type *time.Time"). Search must read rows regardless of the stored format.
func TestLogSearchScansVariedTimestampFormats(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	r := NewLogRepo(db)

	// A normal append (explicit time.Time) must round-trip.
	if err := r.Append(ctx, LogEvent{Component: "c", Action: "normal", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Rows written with assorted stored representations, including ones the
	// driver cannot parse into time.Time on its own.
	raws := []string{
		"2026-06-02T14:12:30Z",          // RFC3339 (clients ship this)
		"2026-06-02 14:12:30",           // SQLite CURRENT_TIMESTAMP text
		"Mon, 02 Jun 2026 14:12:30 UTC", // unparseable -> zero time, not an error
		"",                              // empty -> zero time, not an error
	}
	for i, raw := range raws {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO log_event (occurred_at, component, action) VALUES (?, 'c', ?)`,
			raw, "raw"); err != nil {
			t.Fatalf("insert raw %d: %v", i, err)
		}
	}

	evs, err := r.Search(ctx, LogSearch{})
	if err != nil {
		t.Fatalf("search must not fail on varied timestamp formats: %v", err)
	}
	if len(evs) != 1+len(raws) {
		t.Fatalf("expected %d events, got %d", 1+len(raws), len(evs))
	}
}

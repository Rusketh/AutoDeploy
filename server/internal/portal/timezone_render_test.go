package portal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/runtime"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestFormatTimeHonorsDisplayTimezone verifies the portal's formatTime helper
// renders absolute timestamps in the operator-configured zone (the fix for
// "last check-in shows the wrong time zone"), and falls back to UTC when unset.
func TestFormatTimeHonorsDisplayTimezone(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := runtime.New(ctx, db, nil, config.Config{})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/portal/machines/1", nil)
	when := time.Date(2026, 1, 15, 17, 30, 0, 0, time.UTC) // 12:30 EST

	// Unset -> UTC, friendly format (no raw RFC3339 offset).
	ft := funcsFor(req, Repos{Runtime: st})["formatTime"].(func(time.Time) string)
	if got, want := ft(when), "15 Jan 2026, 17:30:00"; got != want {
		t.Errorf("default formatTime = %q, want UTC %q", got, want)
	}

	// Configured -> rendered in that zone (12:30 EST).
	if err := st.SetDisplayTimezone(ctx, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	ft = funcsFor(req, Repos{Runtime: st})["formatTime"].(func(time.Time) string)
	if got, want := ft(when), "15 Jan 2026, 12:30:00"; got != want {
		t.Errorf("New York formatTime = %q, want %q", got, want)
	}
}

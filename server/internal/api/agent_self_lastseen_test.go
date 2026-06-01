package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// TestAgentSelfBumpsLastSeen verifies a /self poll updates the machine's
// last_seen -- the resident check-in is what should drive "last seen", and it
// previously never touched it (so machines looked offline while polling fine).
func TestAgentSelfBumpsLastSeen(t *testing.T) {
	srv, repos := newSelfTestServer(t)
	ctx := context.Background()

	m, err := repos.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "ls-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repos.Inventory.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}

	// CURRENT_TIMESTAMP has 1s granularity in SQLite; wait so an update is
	// observable rather than collapsing onto the same second.
	time.Sleep(1100 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/v1/agent/self?id=" + m.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after, err := repos.Inventory.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeen.After(before.LastSeen) {
		t.Errorf("last_seen not advanced by /self poll: before=%s after=%s",
			before.LastSeen, after.LastSeen)
	}
}

package notify

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestOfflineWatcherNotifiesOncePerEpisode drives the watcher end-to-end
// through a real emitter: a stale machine produces exactly one in-portal
// notification, a repeat tick produces none, and a check-in re-arms it.
func TestOfflineWatcherNotifiesOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	inv := model.NewInventoryRepo(db)
	notifications := model.NewNotificationRepo(db)
	users := auth.New(db)
	user, err := users.CreateUser(ctx, "op", "pw123456")
	if err != nil {
		t.Fatal(err)
	}

	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	// Stale for 45 minutes against a 30-minute threshold.
	if _, err := db.ExecContext(ctx,
		`UPDATE machine_record SET last_seen=datetime('now','-45 minutes') WHERE id=?`,
		int64(m.ID)); err != nil {
		t.Fatal(err)
	}

	em := &Emitter{Notifications: notifications, Users: users}
	em.Start()
	w := &OfflineWatcher{Inventory: inv, Emitter: em, ThresholdMinutes: func() int { return 30 }}

	w.runOnce(ctx)
	w.runOnce(ctx) // second tick of the same episode must not re-notify
	em.Stop()      // drain the async fan-out before asserting

	if n, _ := notifications.UnreadCount(ctx, user.ID); n != 1 {
		t.Fatalf("unread = %d, want exactly 1 (one episode, one notification)", n)
	}
	list, _, _ := notifications.List(ctx, model.NotificationSearch{UserID: user.ID})
	if len(list) != 1 || list[0].Event != EventMachineOffline {
		t.Fatalf("unexpected notifications: %+v", list)
	}

	// The machine checks in -> episode closes -> goes stale again -> a new
	// episode notifies again.
	if err := inv.TouchLastSeen(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE machine_record SET last_seen=datetime('now','-45 minutes') WHERE id=?`,
		int64(m.ID)); err != nil {
		t.Fatal(err)
	}
	em2 := &Emitter{Notifications: notifications, Users: users}
	em2.Start()
	w.Emitter = em2
	w.runOnce(ctx)
	em2.Stop()
	if n, _ := notifications.UnreadCount(ctx, user.ID); n != 2 {
		t.Fatalf("unread = %d, want 2 after a second offline episode", n)
	}
}

func TestBulkOutcomeEventClassification(t *testing.T) {
	op := model.BulkOperation{ID: 7, Name: "Nightly re-image"}
	cases := []struct {
		p    model.BulkProgress
		want string
	}{
		{model.BulkProgress{Total: 3, OK: 3}, EventBulkCompleted},
		{model.BulkProgress{Total: 3, Failed: 3}, EventBulkFailed},
		{model.BulkProgress{Total: 3, OK: 2, Failed: 1}, EventBulkPartial},
		{model.BulkProgress{Total: 3, OK: 2, Cancelled: 1}, EventBulkPartial},
		{model.BulkProgress{Total: 2, Cancelled: 2}, EventBulkFailed},
	}
	for _, c := range cases {
		if got := BulkOutcomeEvent(op, c.p); got.Name != c.want {
			t.Errorf("progress %+v -> %s, want %s", c.p, got.Name, c.want)
		}
	}
}

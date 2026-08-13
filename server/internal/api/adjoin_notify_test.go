package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestAgentIdentityADJoinFailureAlerts drives the agent identity report with a
// failed AD join and asserts the server persists the status and emits exactly
// one ad.join_failed notification — even across repeated reports of the same
// failure (deduped once per episode).
func TestAgentIdentityADJoinFailureAlerts(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	inv := model.NewInventoryRepo(db)
	bulk := model.NewBulkRepo(db, inv)
	notifications := model.NewNotificationRepo(db)
	users := auth.New(db)
	emitter := &notify.Emitter{Notifications: notifications, Users: users}
	emitter.Start()

	repos := Repos{Inventory: inv, Bulk: bulk, Users: users, Notifications: notifications, Emitter: emitter}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The agent endpoints are unauthenticated (keyed by agent_id), so this test
	// deliberately avoids authedClient — logging in would consume one of the
	// package-global login-limiter's attempts and could throttle other tests.
	// The admin user still needs to exist so the emitter has an in-portal
	// recipient to deliver notifications to.
	if _, err := users.CreateUser(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	client := srv.Client()

	// Enroll a machine and learn its agent_id.
	resp, err := client.Post(srv.URL+"/api/v1/agent/checkin", "application/json",
		strings.NewReader(`{"identity":{"system_uuid":"u-adjoin"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var checkin struct {
		MachineID model.ID `json:"machine_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&checkin)
	resp.Body.Close()
	m, err := inv.Get(ctx, checkin.MachineID)
	if err != nil {
		t.Fatal(err)
	}

	postIdentity := func(status, detail string) {
		body := `{"agent_id":"` + m.AgentID + `","computer_name":"LAB-1",` +
			`"ad_join_status":"` + status + `","ad_join_detail":"` + detail + `"}`
		r, perr := client.Post(srv.URL+"/api/v1/agent/identity", "application/json", strings.NewReader(body))
		if perr != nil {
			t.Fatal(perr)
		}
		if r.StatusCode != http.StatusOK {
			t.Fatalf("identity status = %d", r.StatusCode)
		}
		r.Body.Close()
	}

	// Two reports of the same failure: only the first should alert.
	postIdentity(model.ADJoinFailed, "Add-Computer failed")
	postIdentity(model.ADJoinFailed, "Add-Computer failed")

	emitter.Stop() // drain the async fan-out before asserting

	// Status persisted.
	got, err := inv.Get(ctx, checkin.MachineID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ADJoinStatus != model.ADJoinFailed {
		t.Errorf("stored ADJoinStatus = %q, want %q", got.ADJoinStatus, model.ADJoinFailed)
	}

	all, err := users.ListUsers(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("expected one admin user, got %v err=%v", all, err)
	}
	list, _, err := notifications.List(ctx, model.NotificationSearch{UserID: all[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, n := range list {
		if n.Event == notify.EventADJoinFailed {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ad.join_failed notifications = %d, want exactly 1 (deduped)", count)
	}
}

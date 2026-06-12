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

// TestBulkLifecycleEmitsNotifications drives the real HTTP surface through a
// bulk operation's life — create, agent check-in (first contact + claim),
// job result — and asserts the notification bus produced the documented
// events: machine.first_seen, bulk.created, bulk.completed.
func TestBulkLifecycleEmitsNotifications(t *testing.T) {
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

	repos := Repos{
		Inventory:     inv,
		Bulk:          bulk,
		Users:         users,
		Notifications: notifications,
		Emitter:       emitter,
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := authedClient(t, srv, repos)

	// The machine enrolls (first contact) via agent check-in.
	resp, err := client.Post(srv.URL+"/api/v1/agent/checkin", "application/json",
		strings.NewReader(`{"identity":{"system_uuid":"u-notify"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var checkin struct {
		MachineID model.ID `json:"machine_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&checkin)
	resp.Body.Close()

	// Operator queues a run-now script against it.
	resp, err = client.Post(srv.URL+"/api/v1/bulk/operations", "application/json",
		strings.NewReader(`{"action":"script","payload":"{\"shell\":\"cmd\",\"body\":\"echo hi\"}",
			"target":{"machine_ids":[`+itoa64(int64(checkin.MachineID))+`]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Agent claims and reports success.
	resp, err = client.Post(srv.URL+"/api/v1/agent/checkin", "application/json",
		strings.NewReader(`{"identity":{"system_uuid":"u-notify"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Jobs []model.BulkJob `json:"jobs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&claim)
	resp.Body.Close()
	if len(claim.Jobs) != 1 {
		t.Fatalf("expected to claim 1 job, got %d", len(claim.Jobs))
	}
	resp, err = client.Post(srv.URL+"/api/v1/agent/jobs/"+itoa64(int64(claim.Jobs[0].ID))+"/result",
		"application/json", strings.NewReader(`{"status":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	emitter.Stop() // drain the async fan-out before asserting

	all, err := users.ListUsers(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("expected the one admin user, got %v err=%v", all, err)
	}
	list, _, err := notifications.List(ctx, model.NotificationSearch{UserID: all[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, n := range list {
		got[n.Event]++
	}
	for _, want := range []string{
		notify.EventMachineFirstSeen,
		notify.EventBulkCreated,
		notify.EventBulkCompleted,
	} {
		if got[want] != 1 {
			t.Errorf("event %s: %d notification(s), want 1 (all: %v)", want, got[want], got)
		}
	}
}

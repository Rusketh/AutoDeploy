package model

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestParseLogQuery(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "AAAA-1111", SystemSerial: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = inv.UpsertBinding(ctx, MachineBinding{MachineID: m.ID, MachineName: "LAB-01"})
	london, _ := time.LoadLocation("Europe/London")

	// From/To are display-zone wall times; To includes its whole minute.
	q := ParseLogQuery(ctx, url.Values{"from": {"2026-07-01T09:00"}, "to": {"2026-07-01T10:00"}}, inv, london)
	if got := q.Search.Since.UTC().Format(time.RFC3339); got != "2026-07-01T08:00:00Z" {
		t.Errorf("from in BST: %s", got)
	}
	if q.Search.Until.Sub(q.Search.Since) <= time.Hour || q.From != "2026-07-01T09:00" {
		t.Errorf("to / echo: %+v", q)
	}
	// Old parameters keep working (since read as UTC, as the old page sent it).
	q = ParseLogQuery(ctx, url.Values{"since": {"2026-07-01T09:00"}, "action": {"x.y"}, "limit": {"50"}}, inv, london)
	if q.Search.Since.UTC().Hour() != 9 || q.Search.Action != "x.y" || q.Search.Limit != 50 || !q.Active {
		t.Errorf("legacy: %+v", q.Search)
	}
	// Machine by name, UUID (any case) or ID.
	for _, ref := range []string{"lab-01", "aaaa-1111", "1"} {
		q = ParseLogQuery(ctx, url.Values{"machine": {ref}}, inv, nil)
		if q.MachineRec == nil || q.MachineRec.ID != m.ID || q.Search.Machine == nil || q.Search.Machine.ID != m.ID {
			t.Errorf("machine %q: %+v", ref, q.Search.Machine)
		}
	}
	q = ParseLogQuery(ctx, url.Values{"machine": {"nope"}}, inv, nil)
	if !q.MachineUnknown || q.Search.Machine == nil {
		t.Errorf("unknown machine should still filter: %+v", q)
	}
	q = ParseLogQuery(ctx, url.Values{"level": {"problems"}, "category": {"software"}}, inv, nil)
	if len(q.Search.Levels) != 2 || q.Search.Category != LogCatSoftware {
		t.Errorf("level/category: %+v", q.Search)
	}
	if v := q.Values(); v.Get("level") != "problems" || v.Get("category") != "Software" {
		t.Errorf("values: %v", v)
	}
	if u := FailedLoginLogsURL("bob"); u != "/portal/logs?action=auth.login.fail&q=bob" {
		t.Errorf("failed login url %s", u)
	}
}

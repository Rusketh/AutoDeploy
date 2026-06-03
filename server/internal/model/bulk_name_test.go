package model

import (
	"context"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// TestBulkNameDefaultsAndOverride covers the operator-facing name/description:
// a sensible default is generated from the action + target when blank, an
// explicit value is kept verbatim, and editing with a blank name regenerates
// the default.
func TestBulkNameDefaultsAndOverride(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	var ids []ID
	for _, u := range []string{"n1", "n2", "n3"} {
		rec, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: u})
		ids = append(ids, rec.ID)
	}

	// Blank name/description -> defaults from action + target (3 machines).
	op, _, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:  BulkActionReimage,
		Payload: "{}",
		Target:  BulkTarget{MachineIDs: ids},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Name != "Re-image — 3 machines" {
		t.Errorf("default name = %q", op.Name)
	}
	if !strings.Contains(op.Description, "Re-image on 3 machines") || !strings.Contains(op.Description, "run immediately") {
		t.Errorf("default description = %q", op.Description)
	}
	// Round-trips through the DB unchanged.
	got, _, _ := bulk.GetOperation(ctx, op.ID)
	if got.Name != op.Name || got.Description != op.Description {
		t.Errorf("persisted name/description mismatch: %q / %q", got.Name, got.Description)
	}

	// Explicit name + description are kept verbatim (whitespace trimmed).
	op2, _, err := bulk.CreateOperation(ctx, BulkOperation{
		Name:        "  Nightly lab rebuild  ",
		Description: "  Re-image the lab every night.  ",
		Action:      BulkActionScript,
		Payload:     `{"shell":"cmd","body":"echo hi"}`,
		Target:      BulkTarget{MachineIDs: ids[:1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op2.Name != "Nightly lab rebuild" || op2.Description != "Re-image the lab every night." {
		t.Errorf("custom name/description not kept: %q / %q", op2.Name, op2.Description)
	}

	// A filter-targeted recurring task names itself from the filter.
	op3, _, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:       BulkActionScript,
		Payload:      `{"shell":"cmd","body":"echo hi"}`,
		ScheduleKind: BulkScheduleRecurring,
		TargetMode:   BulkTargetFilter,
		RecurSpec:    `{"every":1,"unit":"day","at":"02:00"}`,
		Target:       BulkTarget{OU: "OU=Lab,DC=corp,DC=example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op3.Name != "Run script — OU OU=Lab,DC=corp,DC=example" {
		t.Errorf("filter default name = %q", op3.Name)
	}

	// Editing a scheduled op with a blank name regenerates the default; a
	// supplied name is kept.
	if err := bulk.UpdateOperation(ctx, BulkOperation{
		ID: op3.ID, Action: BulkActionScript, Payload: `{"shell":"cmd","body":"echo hi"}`,
		ScheduleKind: BulkScheduleRecurring, TargetMode: BulkTargetFilter,
		RecurSpec: `{"every":1,"unit":"day","at":"02:00"}`,
		Target:    BulkTarget{Group: "Lab-Computers"},
		Name:      "", // blank -> regenerate
	}); err != nil {
		t.Fatal(err)
	}
	edited, _, _ := bulk.GetOperation(ctx, op3.ID)
	if edited.Name != "Run script — group Lab-Computers" {
		t.Errorf("edited default name = %q", edited.Name)
	}
}

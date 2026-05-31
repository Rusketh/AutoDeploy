package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestDeleteMachineCascades(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	img := NewImageRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "del-1"})
	i, _ := img.Create(ctx, Image{Name: "base"})
	imgID := i.ID
	_ = inv.UpsertBinding(ctx, MachineBinding{MachineID: m.ID, ImageID: &imgID, MachineName: "LAB-X"})
	depID, _ := inv.RecordDeployment(ctx, m.ID, &imgID)
	_ = inv.CompleteDeployment(ctx, depID, "ok", "")

	if err := inv.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	// Machine is gone.
	if _, err := inv.Get(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("machine should be gone, got %v", err)
	}
	// Referencing rows are gone too.
	if hist, _ := inv.HistoryFor(ctx, m.ID); len(hist) != 0 {
		t.Errorf("history should be empty after delete, got %d", len(hist))
	}
}

func TestUpdateLatestDeployment(t *testing.T) {
	ctx := context.Background()
	inv := NewInventoryRepo(openTestDB(t))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "upd-1"})
	_, _ = inv.RecordDeployment(ctx, m.ID, nil)

	if err := inv.UpdateLatestDeployment(ctx, m.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	hist, _ := inv.HistoryFor(ctx, m.ID)
	if len(hist) != 1 || hist[0].Outcome != "failed" || hist[0].Notes != "boom" {
		t.Errorf("latest deployment not updated: %+v", hist)
	}
}

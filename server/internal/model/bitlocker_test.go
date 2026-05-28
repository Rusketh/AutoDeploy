package model

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func openBL(t *testing.T) (*InventoryRepo, *BitLockerRepo, *secrets.Box) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	return NewInventoryRepo(db), NewBitLockerRepo(db, bx), bx
}

func TestBitLockerPINSetAndRetrieve(t *testing.T) {
	ctx := context.Background()
	inv, bl, _ := openBL(t)
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})

	if st, _ := bl.PINStatus(ctx, m.ID); st.PINSet {
		t.Error("PIN should be unset initially")
	}
	if err := bl.SetPIN(ctx, m.ID, "654321"); err != nil {
		t.Fatal(err)
	}
	if st, _ := bl.PINStatus(ctx, m.ID); !st.PINSet {
		t.Error("PIN should be set")
	}
	got, err := bl.RetrievePIN(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "654321" {
		t.Errorf("PIN round-trip: %q", got)
	}
	// Clear.
	if err := bl.SetPIN(ctx, m.ID, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := bl.PINStatus(ctx, m.ID); st.PINSet {
		t.Error("PIN should be cleared")
	}
}

func TestRecoveryKeyHistoryAppendOnly(t *testing.T) {
	ctx := context.Background()
	inv, bl, _ := openBL(t)
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})

	for i, k := range []string{"keyA", "keyB", "keyC"} {
		if err := bl.EscrowRecoveryKey(ctx, m.ID, k, "deploy"+string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := bl.ListRecoveryKeys(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(hist))
	}
	// Newest first.
	if hist[0].Note != "deploy2" {
		t.Errorf("expected newest first, got %+v", hist[0])
	}
	// List does not return key values.
	for _, h := range hist {
		if h.Key != "" {
			t.Errorf("list should not include key value: %+v", h)
		}
	}
	// Retrieve does.
	got, err := bl.RetrieveRecoveryKey(ctx, hist[2].ID) // oldest
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "keyA" {
		t.Errorf("expected keyA, got %q", got.Key)
	}
}

func TestEmptyKeyRejected(t *testing.T) {
	inv, bl, _ := openBL(t)
	m, _ := inv.UpsertFromIdentity(context.Background(), match.Identity{SystemUUID: "u1"})
	if err := bl.EscrowRecoveryKey(context.Background(), m.ID, "", ""); !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error, got %v", err)
	}
}

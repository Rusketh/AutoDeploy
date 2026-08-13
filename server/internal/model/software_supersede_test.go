package model

import (
	"context"
	"errors"
	"testing"
)

// TestResolveOrderSupersedence covers the "drop the predecessor when the
// successor is present" model: a package that succeeds another suppresses that
// other package whenever both land in the same resolved install set.
func TestResolveOrderSupersedence(t *testing.T) {
	ctx := context.Background()
	repo := NewSoftwarePackageRepo(openTestDB(t))

	v1, _ := repo.Create(ctx, SoftwarePackage{Name: "app-v1"})
	v2, _ := repo.Create(ctx, SoftwarePackage{Name: "app-v2", SucceedsID: &v1.ID})
	other, _ := repo.Create(ctx, SoftwarePackage{Name: "other"})

	// SucceedsID round-trips through the DB.
	if got, _ := repo.Get(ctx, v2.ID); got.SucceedsID == nil || *got.SucceedsID != v1.ID {
		t.Fatalf("v2.SucceedsID = %v, want %d", func() any {
			if got.SucceedsID == nil {
				return nil
			}
			return *got.SucceedsID
		}(), v1.ID)
	}

	// Both v1 and v2 in the set: v1 is superseded and dropped.
	order, warns := repo.ResolveOrder(ctx, []ID{v1.ID, v2.ID, other.ID})
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if has(order, v1.ID) {
		t.Errorf("v1 should be dropped (superseded by v2), got order %v", order)
	}
	if !has(order, v2.ID) || !has(order, other.ID) {
		t.Errorf("v2 and other should remain, got %v", order)
	}

	// v1 alone (successor NOT in the set) still installs — supersedence only
	// takes effect when the superseding package is actually deployed.
	only, _ := repo.ResolveOrder(ctx, []ID{v1.ID})
	if len(only) != 1 || only[0] != v1.ID {
		t.Errorf("v1 alone should install, got %v", only)
	}
}

// TestResolveOrderSupersedenceTransitive checks that a supersedence chain drops
// every older package present, even when an intermediate link is absent from
// the install set.
func TestResolveOrderSupersedenceTransitive(t *testing.T) {
	ctx := context.Background()
	repo := NewSoftwarePackageRepo(openTestDB(t))

	v1, _ := repo.Create(ctx, SoftwarePackage{Name: "v1"})
	v2, _ := repo.Create(ctx, SoftwarePackage{Name: "v2", SucceedsID: &v1.ID})
	v3, _ := repo.Create(ctx, SoftwarePackage{Name: "v3", SucceedsID: &v2.ID})

	// Set holds v1 and v3 but NOT v2. v3 transitively succeeds v1, so v1 drops.
	order, warns := repo.ResolveOrder(ctx, []ID{v1.ID, v3.ID})
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if has(order, v1.ID) || has(order, v2.ID) {
		t.Errorf("only v3 should survive, got %v", order)
	}
	if len(order) != 1 || order[0] != v3.ID {
		t.Errorf("order = %v, want [v3]", order)
	}
}

// TestSucceedsValidation rejects self-supersedence and supersedence cycles.
func TestSucceedsValidation(t *testing.T) {
	ctx := context.Background()
	repo := NewSoftwarePackageRepo(openTestDB(t))

	a, _ := repo.Create(ctx, SoftwarePackage{Name: "a"})
	b, _ := repo.Create(ctx, SoftwarePackage{Name: "b", SucceedsID: &a.ID})

	// a cannot succeed itself.
	if err := repo.Update(ctx, SoftwarePackage{ID: a.ID, Name: "a", SucceedsID: &a.ID}); !errors.Is(err, ErrValidation) {
		t.Errorf("self-succeed should be rejected, got %v", err)
	}

	// a succeeds b while b already succeeds a -> cycle.
	if err := repo.Update(ctx, SoftwarePackage{ID: a.ID, Name: "a", SucceedsID: &b.ID}); !errors.Is(err, ErrValidation) {
		t.Errorf("supersedence cycle should be rejected, got %v", err)
	}
}

// TestDeleteSupersededPackageClearsPointer verifies deleting the superseded
// package nulls the successor's pointer (ON DELETE SET NULL) rather than
// blocking the delete.
func TestDeleteSupersededPackageClearsPointer(t *testing.T) {
	ctx := context.Background()
	repo := NewSoftwarePackageRepo(openTestDB(t))

	v1, _ := repo.Create(ctx, SoftwarePackage{Name: "v1"})
	v2, _ := repo.Create(ctx, SoftwarePackage{Name: "v2", SucceedsID: &v1.ID})

	if err := repo.Delete(ctx, v1.ID); err != nil {
		t.Fatalf("delete superseded package failed: %v", err)
	}
	got, err := repo.Get(ctx, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SucceedsID != nil {
		t.Errorf("v2.SucceedsID should be nil after v1 delete, got %v", *got.SucceedsID)
	}
}

func has(ids []ID, id ID) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

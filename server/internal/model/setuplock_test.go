package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestSetupLockRepo(t *testing.T) {
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	images := NewImageRepo(db)
	img, err := images.Create(ctx, Image{Name: "img"})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSetupLockRepo(db)

	// Unset: Get -> ErrNotFound, Enabled -> false (the common case).
	if _, err := repo.Get(ctx, img.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on unset = %v, want ErrNotFound", err)
	}
	if on, err := repo.Enabled(ctx, img.ID); err != nil || on {
		t.Fatalf("Enabled on unset = (%v,%v), want (false,nil)", on, err)
	}

	// Enable.
	if err := repo.Set(ctx, ImageSetupLock{ImageID: img.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.ImageID != img.ID {
		t.Errorf("Get = %+v, want enabled for image %d", got, img.ID)
	}
	if on, err := repo.Enabled(ctx, img.ID); err != nil || !on {
		t.Fatalf("Enabled after enable = (%v,%v), want (true,nil)", on, err)
	}

	// Disable: the row survives the toggle and Enabled reads false again.
	if err := repo.Set(ctx, ImageSetupLock{ImageID: img.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if on, err := repo.Enabled(ctx, img.ID); err != nil || on {
		t.Fatalf("Enabled after disable = (%v,%v), want (false,nil)", on, err)
	}
}

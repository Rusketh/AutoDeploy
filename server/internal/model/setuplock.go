package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ImageSetupLock is an image's "setup lockout" toggle. When Enabled, machines
// deployed from this image show a branded full-screen setup screen instead of
// the Windows logon screen while the agent performs the INITIAL software
// rollout, blocking sign-in until the machine is ready for first use. A
// technician can dismiss it with the global Access PIN.
//
// The lock applies ONLY during the initial deployment (the agent clears it
// once it closes the open deployment), so later software pushes to an
// already-provisioned machine never lock it. Unlike domain join there is no
// secret here -- it is a single boolean.
type ImageSetupLock struct {
	ImageID   ID        `json:"image_id"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetupLockRepo stores the per-image setup-lockout toggle.
type SetupLockRepo struct {
	db *storage.DB
}

// NewSetupLockRepo binds the repo to a storage handle.
func NewSetupLockRepo(db *storage.DB) *SetupLockRepo {
	return &SetupLockRepo{db: db}
}

// Get returns the image's setup-lock config. ErrNotFound when none is set
// (the common case: most images don't lock).
func (r *SetupLockRepo) Get(ctx context.Context, imageID ID) (ImageSetupLock, error) {
	var v ImageSetupLock
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		SELECT image_id, enabled, updated_at
		FROM image_setup_lock WHERE image_id=?`, imageID).Scan(
		&v.ImageID, &enabled, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageSetupLock{ImageID: imageID}, fmt.Errorf("setup lock for image %d: %w", imageID, ErrNotFound)
	}
	if err != nil {
		return ImageSetupLock{}, err
	}
	v.Enabled = enabled != 0
	return v, nil
}

// Enabled reports whether the image locks out during initial deployment.
// A missing row (the common case) is reported as false, not an error -- so
// callers that only need the boolean can ignore ErrNotFound. This is what the
// manifest handler and the agent /self handler use to gate the lock.
func (r *SetupLockRepo) Enabled(ctx context.Context, imageID ID) (bool, error) {
	v, err := r.Get(ctx, imageID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v.Enabled, nil
}

// Set upserts the toggle. Disabling keeps the row (cheap, and mirrors the
// domain-join repo's behaviour of surviving a toggle).
func (r *SetupLockRepo) Set(ctx context.Context, cfg ImageSetupLock) error {
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO image_setup_lock (image_id, enabled, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(image_id) DO UPDATE SET
		    enabled=excluded.enabled, updated_at=CURRENT_TIMESTAMP`,
		cfg.ImageID, enabled)
	return err
}

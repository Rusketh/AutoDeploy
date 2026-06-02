package model

import (
	"context"
	"database/sql"
	"time"
)

// Re-image trigger sources recorded in reimage_event.source.
const (
	ReimageSourceRemoteFlag = "remote_flag" // portal/bulk remote re-image
	ReimageSourceBootMenu   = "boot_menu"   // operator picked Re-image at boot
)

// ReimageEvent is one concise entry in a machine's re-image history: when the
// machine was rebuilt and what triggered it. It is distinct from
// DeploymentRecord (which logs every deploy, including first installs) — a
// ReimageEvent is written only when a machine that already had an OS is
// re-imaged.
type ReimageEvent struct {
	ID        ID        `json:"id"`
	MachineID ID        `json:"machine_id"`
	ImageID   *ID       `json:"image_id,omitempty"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordReimageEvent appends a re-image history row. Best-effort: callers log
// the deploy regardless of whether this succeeds.
func (r *InventoryRepo) RecordReimageEvent(ctx context.Context, machineID ID, imageID *ID, source string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO reimage_event (machine_id, image_id, source) VALUES (?, ?, ?)`,
		machineID, nullID(imageID), source)
	return err
}

// ListReimageEvents returns a machine's re-image history, newest first.
func (r *InventoryRepo) ListReimageEvents(ctx context.Context, machineID ID) ([]ReimageEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, image_id, source, created_at
		FROM reimage_event WHERE machine_id=?
		ORDER BY created_at DESC, id DESC`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReimageEvent
	for rows.Next() {
		var e ReimageEvent
		var imageID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.MachineID, &imageID, &e.Source, &e.CreatedAt); err != nil {
			return nil, err
		}
		if imageID.Valid {
			id := ID(imageID.Int64)
			e.ImageID = &id
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasPriorDeployment reports whether the machine has any deployment_history
// row that reached a terminal outcome ("ok" or "failed") — i.e. it already
// had (or attempted) an OS install before. Used to classify a new staging as
// a re-image rather than a first-time deploy. The just-opened in_progress row
// for the current deploy is excluded by the outcome filter.
func (r *InventoryRepo) HasPriorDeployment(ctx context.Context, machineID ID) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM deployment_history
		WHERE machine_id=? AND outcome IN ('ok','failed')`, machineID).Scan(&n)
	return n > 0, err
}

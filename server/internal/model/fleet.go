package model

import (
	"context"
	"database/sql"
)

// LatestDeployments returns each machine's most recent deployment row (by
// started_at, newest id on a tie), keyed by machine ID, in one query. The
// machine list and dashboard derive every machine's fleet status from it
// without loading full histories. Machines that never deployed are absent.
func (r *InventoryRepo) LatestDeployments(ctx context.Context) (map[ID]DeploymentRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes, phase, progress_done, progress_total
		FROM deployment_history d
		WHERE d.id = (SELECT d2.id FROM deployment_history d2 WHERE d2.machine_id = d.machine_id
		              ORDER BY d2.started_at DESC, d2.id DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]DeploymentRecord{}
	for rows.Next() {
		var d DeploymentRecord
		var imageID sql.NullInt64
		var completed sql.NullTime
		if err := rows.Scan(&d.ID, &d.MachineID, &imageID, &d.StartedAt,
			&completed, &d.Outcome, &d.Notes, &d.Phase, &d.ProgressDone, &d.ProgressTotal); err != nil {
			return nil, err
		}
		d.ImageID = idPtr(imageID)
		if completed.Valid {
			t := completed.Time
			d.CompletedAt = &t
		}
		out[d.MachineID] = d
	}
	return out, rows.Err()
}

// ReimagePendingSet returns every machine currently flagged for re-image,
// fleet-wide (ListReimagePending is the per-page variant).
func (r *InventoryRepo) ReimagePendingSet(ctx context.Context) (map[ID]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM machine_record WHERE reimage_pending=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]bool{}
	for rows.Next() {
		var id ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// validateActionPayload applies action-specific checks beyond "is JSON".
// Today only the toolkit action has a structured payload worth checking: the
// kit must exist and the Run As override must be well formed.
func (r *BulkRepo) validateActionPayload(ctx context.Context, action, payload string) error {
	if action != BulkActionToolkit {
		return nil
	}
	p, err := ParseToolkitPayload(payload)
	if err != nil {
		return err
	}
	var one int
	err = r.db.QueryRowContext(ctx, `SELECT 1 FROM toolkit WHERE id=?`, int64(p.ToolkitID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: tool kit %d does not exist", ErrValidation, p.ToolkitID)
	}
	return err
}

// filterToolkitTargets drops, for a toolkit operation whose payload asks to
// skip machines that already ran the kit, every machine with a successful
// job for that kit -- or one still queued/running -- in ANY operation. That
// makes a recurring or re-evaluated operation run the kit at most once per
// machine. Other actions (and toolkit ops without the flag) pass through.
//
// Runs outside the job-insert transaction: the pool holds one connection.
func (r *BulkRepo) filterToolkitTargets(ctx context.Context, action, payload string, targets []MachineRecord) ([]MachineRecord, error) {
	if action != BulkActionToolkit || len(targets) == 0 {
		return targets, nil
	}
	p, err := ParseToolkitPayload(payload)
	if err != nil || !p.SkipSucceeded {
		return targets, nil
	}
	done, err := r.ToolkitDoneMachines(ctx, p.ToolkitID)
	if err != nil {
		return nil, err
	}
	out := targets[:0:0]
	for _, m := range targets {
		if !done[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// ToolkitDoneMachines returns the machines that have a successful job for
// the kit, or one still queued/running.
func (r *BulkRepo) ToolkitDoneMachines(ctx context.Context, toolkitID ID) (map[ID]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT j.machine_id
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE `+toolkitOpFilter+` AND j.status IN ('ok','queued','running')`,
		int64(toolkitID))
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

// PurgeFinishedCredentials is the scheduler's backstop sweep: it deletes the
// Run As password override of every completed or cancelled operation.
func (r *BulkRepo) PurgeFinishedCredentials(ctx context.Context) (int64, error) {
	return PurgeFinishedCredentials(ctx, r.db)
}

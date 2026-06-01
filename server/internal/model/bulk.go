package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// BulkAction is the discriminator on a bulk operation's payload.
const (
	BulkActionRename       = "rename"
	BulkActionSoftwarePush = "software_push"
	BulkActionScript       = "script"
	// BulkActionReimage flags the machine to re-image and reboots it. The
	// flag is set on the machine record (SetReimagePending) when the
	// operation is created, not carried in the job payload; the job just
	// tells the agent to reboot. Payload is "{}".
	BulkActionReimage = "reimage"
)

// BulkTarget is the AD-centric selection. Empty fields are ignored.
type BulkTarget struct {
	NameRegex string `json:"name_regex,omitempty"`
	OU        string `json:"ou,omitempty"`
	Group     string `json:"group,omitempty"`
	// MachineIDs selects specific machines directly (used by the
	// per-machine "run on this machine" actions). When set, only these
	// machines are considered (AND-combined with any other filters).
	MachineIDs []ID `json:"machine_ids,omitempty"`
}

// BulkOperation is the operator's intent.
type BulkOperation struct {
	ID        ID         `json:"id"`
	Action    string     `json:"action"`
	Payload   string     `json:"payload"`
	Target    BulkTarget `json:"target"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	// ReimageImageID is the image to deploy for a reimage operation; 0
	// means "use each machine's existing binding". Set by the caller for
	// BulkActionReimage; not persisted on the operation row (it's applied
	// to each target's machine_record flag).
	ReimageImageID ID `json:"reimage_image_id,omitempty"`
}

// BulkJob is one queued unit of work, per-machine.
type BulkJob struct {
	ID          ID         `json:"id"`
	OperationID ID         `json:"operation_id"`
	MachineID   ID         `json:"machine_id"`
	Action      string     `json:"action"`
	Payload     string     `json:"payload"`
	Status      string     `json:"status"`
	ResultJSON  string     `json:"result_json,omitempty"`
	QueuedAt    time.Time  `json:"queued_at"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// BulkRepo owns bulk_operation and bulk_job.
type BulkRepo struct {
	db        *storage.DB
	Inventory *InventoryRepo
}

func NewBulkRepo(db *storage.DB, inv *InventoryRepo) *BulkRepo {
	return &BulkRepo{db: db, Inventory: inv}
}

// PreviewTargets resolves the target selection against current inventory
// WITHOUT creating an operation. The portal calls this so an operator can
// see exactly which machines an action will touch.
func (r *BulkRepo) PreviewTargets(ctx context.Context, t BulkTarget) ([]MachineRecord, error) {
	// All machines first, then filter in Go. At the design's scale
	// (thousands of machines max) this is plenty.
	all, err := r.Inventory.List(ctx)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if strings.TrimSpace(t.NameRegex) != "" {
		re, err = regexp.Compile(t.NameRegex)
		if err != nil {
			return nil, fmt.Errorf("%w: name_regex: %v", ErrValidation, err)
		}
	}
	// Explicit machine-id selection (the "selection basket"): when set,
	// only these machines are considered (AND-combined with any filters).
	idSet := map[ID]bool{}
	for _, id := range t.MachineIDs {
		idSet[id] = true
	}
	out := make([]MachineRecord, 0, len(all))
	for _, m := range all {
		if len(idSet) > 0 && !idSet[m.ID] {
			continue
		}
		bind, _ := r.Inventory.GetBinding(ctx, m.ID)
		if re != nil && !re.MatchString(bind.MachineName) {
			continue
		}
		if t.OU != "" && !strings.EqualFold(bind.TargetOU, t.OU) {
			continue
		}
		if t.Group != "" {
			has := false
			for _, g := range bind.GroupMemberships {
				if strings.EqualFold(g, t.Group) {
					has = true
					break
				}
			}
			if !has {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// CreateOperation persists the operation, resolves the targets, and queues
// one bulk_job per targeted machine.
func (r *BulkRepo) CreateOperation(ctx context.Context, op BulkOperation) (BulkOperation, []BulkJob, error) {
	switch op.Action {
	case BulkActionRename, BulkActionSoftwarePush, BulkActionScript, BulkActionReimage:
	default:
		return BulkOperation{}, nil, fmt.Errorf("%w: unknown action %q", ErrValidation, op.Action)
	}
	if !json.Valid([]byte(op.Payload)) {
		return BulkOperation{}, nil, fmt.Errorf("%w: payload must be valid JSON", ErrValidation)
	}
	targetJSON, err := json.Marshal(op.Target)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	targets, err := r.PreviewTargets(ctx, op.Target)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO bulk_operation (action, payload, target_json, created_by) VALUES (?, ?, ?, ?)`,
		op.Action, op.Payload, string(targetJSON), op.CreatedBy)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	opID, _ := res.LastInsertId()
	for _, m := range targets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bulk_job (operation_id, machine_id) VALUES (?, ?)`,
			opID, m.ID); err != nil {
			return BulkOperation{}, nil, err
		}
		// Reimage: flag the machine so the boot client auto-deploys on its
		// next network boot. imageID 0 in the payload means "use binding".
		if op.Action == BulkActionReimage {
			if _, err := tx.ExecContext(ctx,
				`UPDATE machine_record SET reimage_pending=1, reimage_image_id=? WHERE id=?`,
				op.ReimageImageID, m.ID); err != nil {
				return BulkOperation{}, nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return BulkOperation{}, nil, err
	}
	return r.GetOperation(ctx, ID(opID))
}

// GetOperation returns the operation row plus its jobs.
func (r *BulkRepo) GetOperation(ctx context.Context, id ID) (BulkOperation, []BulkJob, error) {
	var v BulkOperation
	var targetJSON string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, action, payload, target_json, created_by, created_at
		FROM bulk_operation WHERE id=?`, id).Scan(
		&v.ID, &v.Action, &v.Payload, &targetJSON, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BulkOperation{}, nil, fmt.Errorf("operation %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return BulkOperation{}, nil, err
	}
	_ = json.Unmarshal([]byte(targetJSON), &v.Target)
	jobs, err := r.jobsFor(ctx, id)
	return v, jobs, err
}

func (r *BulkRepo) jobsFor(ctx context.Context, opID ID) ([]BulkJob, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.id, j.operation_id, j.machine_id, o.action, o.payload,
		       j.status, j.result_json, j.queued_at, j.claimed_at, j.completed_at
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE j.operation_id=?
		ORDER BY j.id`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BulkJob
	for rows.Next() {
		var j BulkJob
		var claimed, completed sql.NullTime
		if err := rows.Scan(&j.ID, &j.OperationID, &j.MachineID, &j.Action, &j.Payload,
			&j.Status, &j.ResultJSON, &j.QueuedAt, &claimed, &completed); err != nil {
			return nil, err
		}
		if claimed.Valid {
			t := claimed.Time
			j.ClaimedAt = &t
		}
		if completed.Valid {
			t := completed.Time
			j.CompletedAt = &t
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimJobsFor returns up to max queued jobs for the machine, marking
// them 'running'. Called by the agent on check-in.
func (r *BulkRepo) ClaimJobsFor(ctx context.Context, machineID ID, max int) ([]BulkJob, error) {
	if max <= 0 {
		max = 8
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT j.id, j.operation_id, j.machine_id, o.action, o.payload,
		       j.status, j.queued_at
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE j.machine_id=? AND j.status='queued'
		ORDER BY j.id
		LIMIT ?`, machineID, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BulkJob
	for rows.Next() {
		var j BulkJob
		if err := rows.Scan(&j.ID, &j.OperationID, &j.MachineID, &j.Action, &j.Payload,
			&j.Status, &j.QueuedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		res, err := tx.ExecContext(ctx,
			`UPDATE bulk_job SET status='running', claimed_at=CURRENT_TIMESTAMP WHERE id=? AND status='queued'`,
			out[i].ID)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue // already claimed by another agent
		}
		out[i].Status = "running"
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Filter out any jobs that were already claimed
	claimed := out[:0]
	for _, j := range out {
		if j.Status == "running" {
			claimed = append(claimed, j)
		}
	}
	return claimed, nil
}

// CompleteJob writes the final status + result.
func (r *BulkRepo) CompleteJob(ctx context.Context, jobID ID, status, resultJSON string) error {
	if status != "ok" && status != "failed" {
		return fmt.Errorf("%w: invalid status %q", ErrValidation, status)
	}
	if resultJSON == "" {
		resultJSON = "{}"
	}
	if !json.Valid([]byte(resultJSON)) {
		return fmt.Errorf("%w: result_json must be valid JSON", ErrValidation)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE bulk_job SET status=?, result_json=?, completed_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, resultJSON, jobID)
	return err
}

// ListOperations returns operations newest first.
func (r *BulkRepo) ListOperations(ctx context.Context) ([]BulkOperation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, action, payload, target_json, created_by, created_at
		FROM bulk_operation ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BulkOperation
	for rows.Next() {
		var v BulkOperation
		var targetJSON string
		if err := rows.Scan(&v.ID, &v.Action, &v.Payload, &targetJSON, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(targetJSON), &v.Target)
		out = append(out, v)
	}
	return out, rows.Err()
}

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TaskRepo is the repository for task rows (reusable scripts/tasks).
type TaskRepo struct{ db *storage.DB }

func NewTaskRepo(db *storage.DB) *TaskRepo { return &TaskRepo{db: db} }

// validateTask normalises defaults and rejects unknown shell/filter kinds.
// Mutates in place so callers persist the normalised form.
func validateTask(in *Task) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	switch strings.TrimSpace(in.PayloadShell) {
	case "", TaskShellPowerShell:
		in.PayloadShell = TaskShellPowerShell
	case TaskShellCmd:
		in.PayloadShell = TaskShellCmd
	default:
		return fmt.Errorf("%w: unknown payload shell %q (allowed: powershell, cmd)", ErrValidation, in.PayloadShell)
	}
	switch strings.TrimSpace(in.FilterType) {
	case TaskFilterNone, TaskFilterOS, TaskFilterWMIC, TaskFilterPS1:
		in.FilterType = strings.TrimSpace(in.FilterType)
	default:
		return fmt.Errorf("%w: unknown filter type %q (allowed: '', os, wmic, ps1)", ErrValidation, in.FilterType)
	}
	// A filter kind with no value can never match — reject rather than silently
	// skipping every machine.
	if in.FilterType != TaskFilterNone && strings.TrimSpace(in.FilterValue) == "" {
		return fmt.Errorf("%w: filter type %q requires a filter value", ErrValidation, in.FilterType)
	}
	if strings.TrimSpace(in.SuccessCodesJSON) == "" {
		in.SuccessCodesJSON = "[0]"
	}
	var codes []int
	if err := json.Unmarshal([]byte(in.SuccessCodesJSON), &codes); err != nil {
		return fmt.Errorf("%w: success_codes_json must be a JSON array of integers: %v", ErrValidation, err)
	}
	return nil
}

func (r *TaskRepo) Create(ctx context.Context, in Task) (Task, error) {
	if err := validateTask(&in); err != nil {
		return Task{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO task
		    (name, description, payload_shell, payload_body,
		     filter_type, filter_value, continue_on_failure, success_codes_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Description, in.PayloadShell, in.PayloadBody,
		in.FilterType, in.FilterValue, boolToInt(in.ContinueOnFailure), in.SuccessCodesJSON)
	if err != nil {
		if isUniqueErr(err) {
			return Task{}, fmt.Errorf("task %q: %w", in.Name, ErrConflict)
		}
		return Task{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

func (r *TaskRepo) Get(ctx context.Context, id ID) (Task, error) {
	var v Task
	var cof int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, payload_shell, payload_body,
		       filter_type, filter_value, continue_on_failure, success_codes_json,
		       created_at, updated_at
		FROM task WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.PayloadShell, &v.PayloadBody,
		&v.FilterType, &v.FilterValue, &cof, &v.SuccessCodesJSON,
		&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, err
	}
	v.ContinueOnFailure = cof != 0
	return v, nil
}

func (r *TaskRepo) List(ctx context.Context) ([]Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, payload_shell, payload_body,
		       filter_type, filter_value, continue_on_failure, success_codes_json,
		       created_at, updated_at
		FROM task ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var v Task
		var cof int
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.PayloadShell,
			&v.PayloadBody, &v.FilterType, &v.FilterValue, &cof, &v.SuccessCodesJSON,
			&v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.ContinueOnFailure = cof != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *TaskRepo) Update(ctx context.Context, in Task) error {
	if err := validateTask(&in); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE task
		SET name=?, description=?, payload_shell=?, payload_body=?,
		    filter_type=?, filter_value=?, continue_on_failure=?, success_codes_json=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.PayloadShell, in.PayloadBody,
		in.FilterType, in.FilterValue, boolToInt(in.ContinueOnFailure), in.SuccessCodesJSON, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("task %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

// Delete removes a task. Refuses while any sequence step still references it
// (ON DELETE RESTRICT enforces this at the DB level too); the portal surfaces
// RefCount so the operator can find the referencing sequences first.
func (r *TaskRepo) Delete(ctx context.Context, id ID) error {
	n, err := r.RefCount(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("task %d: %w (used by %d sequence steps)", id, ErrInUse, n)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM task WHERE id=?`, id)
	if err != nil {
		return err
	}
	m, _ := res.RowsAffected()
	if m == 0 {
		return fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	return nil
}

// RefCount returns how many sequence steps reference this task.
func (r *TaskRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sequence_step WHERE task_id=?`, id).Scan(&n)
	return n, err
}

// Clone creates a copy of the task under a new name.
func (r *TaskRepo) Clone(ctx context.Context, id ID, newName string) (Task, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	src.ID = 0
	src.Name = newName
	return r.Create(ctx, src)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

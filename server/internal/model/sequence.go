package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// SequenceRepo owns CRUD for sequence + sequence_step, including nested-
// sequence cycle prevention (a sequence may embed other sequences, but never
// in a loop).
type SequenceRepo struct{ db *storage.DB }

func NewSequenceRepo(db *storage.DB) *SequenceRepo { return &SequenceRepo{db: db} }

// validateStep checks one step's kind and that the fields its kind needs are
// present. Cross-row validity (referenced task/sequence exists, no cycle) is
// checked separately against the loaded graph.
func validateStep(s SequenceStep) error {
	switch s.Kind {
	case SeqStepBuiltin:
		switch s.BuiltinAction {
		case BuiltinSoftware, BuiltinDomainJoin, BuiltinReboot, BuiltinGPUpdate:
		default:
			return fmt.Errorf("%w: unknown builtin action %q", ErrValidation, s.BuiltinAction)
		}
	case SeqStepTask:
		if s.TaskID == nil || *s.TaskID <= 0 {
			return fmt.Errorf("%w: a task step needs a task_id", ErrValidation)
		}
	case SeqStepSubsequence:
		if s.ChildSequenceID == nil || *s.ChildSequenceID <= 0 {
			return fmt.Errorf("%w: a subsequence step needs a child_sequence_id", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: unknown step kind %q (allowed: builtin, task, subsequence)", ErrValidation, s.Kind)
	}
	return nil
}

func (r *SequenceRepo) Create(ctx context.Context, in Sequence) (Sequence, error) {
	if err := validateName(in.Name); err != nil {
		return Sequence{}, err
	}
	for _, s := range in.Steps {
		if err := validateStep(s); err != nil {
			return Sequence{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Sequence{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO sequence (name, description, owner_image_id)
		VALUES (?, ?, ?)`,
		in.Name, in.Description, nullID(in.OwnerImageID))
	if err != nil {
		if isUniqueErr(err) {
			return Sequence{}, fmt.Errorf("sequence %q: %w", in.Name, ErrConflict)
		}
		return Sequence{}, err
	}
	id64, _ := res.LastInsertId()
	id := ID(id64)
	// A freshly inserted sequence has no incoming edges yet, but a step could
	// still name a not-yet-existent or self child; run the same cycle guard.
	if err := r.checkStepCyclesTx(ctx, tx, id, in.Steps); err != nil {
		return Sequence{}, err
	}
	if err := writeSequenceSteps(ctx, tx, id, in.Steps); err != nil {
		return Sequence{}, err
	}
	if err := tx.Commit(); err != nil {
		return Sequence{}, err
	}
	return r.Get(ctx, id)
}

func (r *SequenceRepo) Update(ctx context.Context, in Sequence) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	for _, s := range in.Steps {
		if err := validateStep(s); err != nil {
			return err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE sequence SET name=?, description=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, in.Name, in.Description, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("sequence %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sequence %d: %w", in.ID, ErrNotFound)
	}
	if err := r.checkStepCyclesTx(ctx, tx, in.ID, in.Steps); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sequence_step WHERE sequence_id=?`, in.ID); err != nil {
		return err
	}
	if err := writeSequenceSteps(ctx, tx, in.ID, in.Steps); err != nil {
		return err
	}
	return tx.Commit()
}

// checkStepCyclesTx rejects a proposed step set for seqID that would create a
// nested-sequence loop: a subsequence step pointing at seqID itself, or at a
// child that can already reach seqID through existing subsequence edges. Also
// validates that referenced tasks and child sequences exist. Runs inside the
// caller's tx so it sees a consistent snapshot.
func (r *SequenceRepo) checkStepCyclesTx(ctx context.Context, tx *sql.Tx, seqID ID, steps []SequenceStep) error {
	// Load every existing child edge EXCEPT seqID's own (we're replacing those).
	rows, err := tx.QueryContext(ctx, `
		SELECT sequence_id, child_sequence_id
		FROM sequence_step
		WHERE kind='subsequence' AND child_sequence_id IS NOT NULL AND sequence_id<>?`, seqID)
	if err != nil {
		return err
	}
	edges := map[ID][]ID{}
	for rows.Next() {
		var sid, cid ID
		if err := rows.Scan(&sid, &cid); err != nil {
			rows.Close()
			return err
		}
		edges[sid] = append(edges[sid], cid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Overlay the proposed children for seqID.
	for _, s := range steps {
		if s.Kind != SeqStepSubsequence || s.ChildSequenceID == nil {
			continue
		}
		c := *s.ChildSequenceID
		if c == seqID {
			return fmt.Errorf("sequence %d: %w (a sequence cannot contain itself)", seqID, ErrCycle)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sequence WHERE id=?`, c).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("%w: subsequence %d does not exist", ErrValidation, c)
		}
		edges[seqID] = append(edges[seqID], c)
	}
	// A child that can already reach seqID would close the loop.
	for _, c := range edges[seqID] {
		if reachableGroup(edges, c, seqID) {
			return fmt.Errorf("sequence %d: %w (nesting sequence %d would create a loop)", seqID, ErrCycle, c)
		}
	}
	// Validate referenced tasks exist (clean ErrValidation rather than a raw FK
	// error mapped to 500).
	for _, s := range steps {
		if s.Kind == SeqStepTask && s.TaskID != nil {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE id=?`, *s.TaskID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("%w: task %d does not exist", ErrValidation, *s.TaskID)
			}
		}
	}
	return nil
}

func writeSequenceSteps(ctx context.Context, tx *sql.Tx, seqID ID, steps []SequenceStep) error {
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sequence_step
			    (sequence_id, order_value, kind, builtin_action, task_id,
			     child_sequence_id, continue_on_failure)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			seqID, s.OrderValue, s.Kind, s.BuiltinAction,
			nullID(s.TaskID), nullID(s.ChildSequenceID), boolToInt(s.ContinueOnFailure)); err != nil {
			return err
		}
	}
	return nil
}

func (r *SequenceRepo) scanSequence(ctx context.Context, scan func(dest ...any) error) (Sequence, error) {
	var v Sequence
	var owner sql.NullInt64
	if err := scan(&v.ID, &v.Name, &v.Description, &owner, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return Sequence{}, err
	}
	v.OwnerImageID = idPtr(owner)
	return v, nil
}

func (r *SequenceRepo) Get(ctx context.Context, id ID) (Sequence, error) {
	v, err := r.scanSequence(ctx, r.db.QueryRowContext(ctx, `
		SELECT id, name, description, owner_image_id, created_at, updated_at
		FROM sequence WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Sequence{}, fmt.Errorf("sequence %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Sequence{}, err
	}
	steps, err := r.stepsFor(ctx, id)
	if err != nil {
		return Sequence{}, err
	}
	v.Steps = steps
	return v, nil
}

// GetOwned returns the inline sequence owned by imageID, or ErrNotFound if the
// image has no inline sequence.
func (r *SequenceRepo) GetOwned(ctx context.Context, imageID ID) (Sequence, error) {
	v, err := r.scanSequence(ctx, r.db.QueryRowContext(ctx, `
		SELECT id, name, description, owner_image_id, created_at, updated_at
		FROM sequence WHERE owner_image_id=?`, imageID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Sequence{}, fmt.Errorf("owned sequence for image %d: %w", imageID, ErrNotFound)
	}
	if err != nil {
		return Sequence{}, err
	}
	steps, err := r.stepsFor(ctx, v.ID)
	if err != nil {
		return Sequence{}, err
	}
	v.Steps = steps
	return v, nil
}

// List returns the shared (library) sequences — image-owned inline sequences
// are excluded — ordered by name.
func (r *SequenceRepo) List(ctx context.Context) ([]Sequence, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, owner_image_id, created_at, updated_at
		FROM sequence WHERE owner_image_id IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sequence
	for rows.Next() {
		v, err := r.scanSequence(ctx, rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		steps, err := r.stepsFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Steps = steps
	}
	return out, nil
}

func (r *SequenceRepo) stepsFor(ctx context.Context, id ID) ([]SequenceStep, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT order_value, kind, builtin_action, task_id, child_sequence_id, continue_on_failure
		FROM sequence_step
		WHERE sequence_id=?
		ORDER BY order_value, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SequenceStep
	for rows.Next() {
		var s SequenceStep
		var task, child sql.NullInt64
		var cof int
		if err := rows.Scan(&s.OrderValue, &s.Kind, &s.BuiltinAction, &task, &child, &cof); err != nil {
			return nil, err
		}
		s.TaskID = idPtr(task)
		s.ChildSequenceID = idPtr(child)
		s.ContinueOnFailure = cof != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// Delete removes a shared sequence. Refuses while any image links it, any other
// sequence embeds it as a subsequence, or a child image inherits… only direct
// references are counted here (the same guard RefCount surfaces to the portal).
func (r *SequenceRepo) Delete(ctx context.Context, id ID) error {
	n, err := r.RefCount(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("sequence %d: %w (referenced by %d objects)", id, ErrInUse, n)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM sequence WHERE id=?`, id)
	if err != nil {
		return err
	}
	m, _ := res.RowsAffected()
	if m == 0 {
		return fmt.Errorf("sequence %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteOwned removes an image-owned inline sequence (no ref guard — it is
// private to the image). No-op if the image has none.
func (r *SequenceRepo) DeleteOwned(ctx context.Context, imageID ID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sequence WHERE owner_image_id=?`, imageID)
	return err
}

// RefCount returns how many images link this sequence plus how many other
// sequences embed it as a subsequence.
func (r *SequenceRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var images, embeds int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE sequence_id=?`, id).Scan(&images); err != nil {
		return 0, err
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sequence_step WHERE child_sequence_id=?`, id).Scan(&embeds); err != nil {
		return 0, err
	}
	return images + embeds, nil
}

// Clone copies a shared sequence under a new name (steps preserved).
func (r *SequenceRepo) Clone(ctx context.Context, id ID, newName string) (Sequence, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return Sequence{}, err
	}
	src.ID = 0
	src.Name = newName
	src.OwnerImageID = nil
	return r.Create(ctx, src)
}

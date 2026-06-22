package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RecurSpec is the parsed form of BulkOperation.RecurSpec. Either a preset
// (Every/Unit, with At/Weekday for day/week cadences) or a raw 5-field Cron
// expression. Times are interpreted in the server's local timezone.
type RecurSpec struct {
	Every   int    `json:"every,omitempty"`   // >=1
	Unit    string `json:"unit,omitempty"`    // hour | day | week
	At      string `json:"at,omitempty"`      // "HH:MM" for day/week
	Weekday int    `json:"weekday,omitempty"` // 0=Sun..6=Sat, for week
	Cron    string `json:"cron,omitempty"`    // overrides the preset when set
}

// withScheduleDefaults fills the zero-value lifecycle fields so callers (and
// older API clients) that don't set them get the historical behaviour.
func withScheduleDefaults(op BulkOperation) BulkOperation {
	if op.ScheduleKind == "" {
		op.ScheduleKind = BulkScheduleNow
	}
	if op.TargetMode == "" {
		op.TargetMode = BulkTargetSelection
	}
	return op
}

// validateSchedule checks the schedule fields are internally consistent.
func validateSchedule(op BulkOperation) error {
	switch op.ScheduleKind {
	case BulkScheduleNow:
	case BulkScheduleOnce:
		if op.RunAt == nil || op.RunAt.IsZero() {
			return fmt.Errorf("%w: a one-time schedule needs a run time", ErrValidation)
		}
	case BulkScheduleRecurring:
		if _, err := parseRecur(op.RecurSpec); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown schedule kind %q", ErrValidation, op.ScheduleKind)
	}
	switch op.TargetMode {
	case BulkTargetSelection, BulkTargetFilter:
	default:
		return fmt.Errorf("%w: unknown target mode %q", ErrValidation, op.TargetMode)
	}
	if op.CancelAfterMinutes < 0 {
		return fmt.Errorf("%w: cancel-after must be zero (never) or a positive number of minutes", ErrValidation)
	}
	return nil
}

func parseRecur(spec string) (RecurSpec, error) {
	var rs RecurSpec
	if strings.TrimSpace(spec) == "" {
		return rs, fmt.Errorf("%w: a recurring schedule needs a cadence", ErrValidation)
	}
	if err := json.Unmarshal([]byte(spec), &rs); err != nil {
		return rs, fmt.Errorf("%w: recurrence: %v", ErrValidation, err)
	}
	if strings.TrimSpace(rs.Cron) != "" {
		if _, err := parseCron(rs.Cron); err != nil {
			return rs, fmt.Errorf("%w: cron: %v", ErrValidation, err)
		}
		return rs, nil
	}
	if rs.Every < 1 {
		rs.Every = 1
	}
	switch rs.Unit {
	case "hour", "day", "week":
	default:
		return rs, fmt.Errorf("%w: recurrence unit must be hour, day or week", ErrValidation)
	}
	return rs, nil
}

// firstRun is the next fire time when an operation is first scheduled.
func firstRun(now time.Time, op BulkOperation) (time.Time, error) {
	if op.ScheduleKind == BulkScheduleOnce {
		return *op.RunAt, nil
	}
	rs, err := parseRecur(op.RecurSpec)
	if err != nil {
		return time.Time{}, err
	}
	return nextRun(now, rs)
}

// nextRun returns the first fire time strictly after `after` for a recurrence.
func nextRun(after time.Time, rs RecurSpec) (time.Time, error) {
	if strings.TrimSpace(rs.Cron) != "" {
		return cronNext(after, rs.Cron)
	}
	every := rs.Every
	if every < 1 {
		every = 1
	}
	switch rs.Unit {
	case "hour":
		return after.Add(time.Duration(every) * time.Hour), nil
	case "day":
		h, m := parseHHMM(rs.At)
		c := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, after.Location())
		for !c.After(after) {
			c = c.AddDate(0, 0, every)
		}
		return c, nil
	case "week":
		h, m := parseHHMM(rs.At)
		c := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, after.Location())
		// Align to the requested weekday.
		delta := (rs.Weekday - int(c.Weekday()) + 7) % 7
		c = c.AddDate(0, 0, delta)
		for !c.After(after) {
			c = c.AddDate(0, 0, 7*every)
		}
		return c, nil
	}
	return time.Time{}, fmt.Errorf("%w: unknown recurrence unit %q", ErrValidation, rs.Unit)
}

func parseHHMM(s string) (h, m int) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) == 2 {
		h, _ = strconv.Atoi(parts[0])
		m, _ = strconv.Atoi(parts[1])
	}
	if h < 0 || h > 23 {
		h = 0
	}
	if m < 0 || m > 59 {
		m = 0
	}
	return h, m
}

// RunOperationNow materialises a fresh run of an operation: it resolves the
// target (re-resolving filters / replaying the frozen basket per its stored
// target), queues one job per machine for the new run number, flags re-image
// machines, and marks the operation active. Used by the immediate-create path
// (indirectly) and by the scheduler when an operation comes due.
func (r *BulkRepo) RunOperationNow(ctx context.Context, id ID) ([]BulkJob, error) {
	op, _, err := r.GetOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	targets, err := r.PreviewTargets(ctx, op.Target)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	runNo := op.RunCount + 1

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.insertJobsTx(ctx, tx, id, op.Action, op.ReimageImageID, targets, runNo); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE bulk_operation SET run_count=?, last_run_at=?, status='active' WHERE id=?`,
		runNo, now, int64(id)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_, jobs, err := r.GetOperation(ctx, id)
	return jobs, err
}

// ListDueOperations returns scheduled/recurring operations whose next_run_at
// has arrived.
func (r *BulkRepo) ListDueOperations(ctx context.Context, now time.Time) ([]BulkOperation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+bulkOpColumns+` FROM bulk_operation
		 WHERE status IN ('scheduled','active') AND next_run_at IS NOT NULL AND next_run_at<=?
		 ORDER BY next_run_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BulkOperation
	for rows.Next() {
		v, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AdvanceSchedule sets the next fire time after a run. One-time operations
// clear next_run_at (completion is derived from their jobs); recurring ones
// compute the next slot.
func (r *BulkRepo) AdvanceSchedule(ctx context.Context, id ID, now time.Time) error {
	op, _, err := r.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.ScheduleKind != BulkScheduleRecurring {
		_, err := r.db.ExecContext(ctx,
			`UPDATE bulk_operation SET next_run_at=NULL WHERE id=?`, int64(id))
		return err
	}
	rs, err := parseRecur(op.RecurSpec)
	if err != nil {
		return err
	}
	nr, err := nextRun(now.In(r.location()), rs)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE bulk_operation SET next_run_at=? WHERE id=?`, nr.UTC(), int64(id))
	return err
}

// ExpireOverdueJobs cancels every queued job that has outlived its
// operation's cancel-after window, clears the re-image flag those jobs set
// (unless another live re-image job still wants it), and completes any
// non-recurring operation whose last outstanding job just expired. The bulk
// scheduler calls it each tick; ClaimJobsFor independently refuses expired
// jobs so the deadline holds even between ticks.
//
// Returns the number of jobs cancelled and the IDs of operations this sweep
// flipped to 'completed' (so the caller can emit finished notifications).
func (r *BulkRepo) ExpireOverdueJobs(ctx context.Context, now time.Time) (int64, []ID, error) {
	// Candidate scan outside the transaction (the pool caps at one
	// connection); the UPDATEs below re-check status='queued' so a job
	// claimed in between is left alone.
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.id, j.machine_id, j.queued_at, o.id, o.action, o.cancel_after_minutes
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE j.status='queued' AND o.cancel_after_minutes>0`)
	if err != nil {
		return 0, nil, err
	}
	type expired struct {
		jobID, machineID, opID ID
		action                 string
	}
	var todo []expired
	opSeen := map[ID]bool{}
	var opOrder []ID
	for rows.Next() {
		var e expired
		var queuedAt time.Time
		var cancelAfter int
		if err := rows.Scan(&e.jobID, &e.machineID, &queuedAt, &e.opID, &e.action, &cancelAfter); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if !jobExpired(queuedAt, cancelAfter, now) {
			continue
		}
		todo = append(todo, e)
		if !opSeen[e.opID] {
			opSeen[e.opID] = true
			opOrder = append(opOrder, e.opID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(todo) == 0 {
		return 0, nil, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var cancelled int64
	for _, e := range todo {
		res, err := tx.ExecContext(ctx, `
			UPDATE bulk_job
			   SET status='cancelled',
			       result_json='{"cancelled":"not picked up before the operation''s cancel-after deadline"}',
			       completed_at=CURRENT_TIMESTAMP
			 WHERE id=? AND status='queued'`, int64(e.jobID))
		if err != nil {
			return 0, nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue // claimed since the scan; the deadline already passed claim's filter, so this is a near-miss race — leave it
		}
		cancelled += n
		// An expired re-image must not fire when someone powers the machine
		// on later: drop the boot-time flag, unless a different still-live
		// re-image job (another op, or a newer run) legitimately wants it.
		if e.action == BulkActionReimage {
			if _, err := tx.ExecContext(ctx, `
				UPDATE machine_record SET reimage_pending=0, reimage_image_id=0
				WHERE id=? AND NOT EXISTS (
				  SELECT 1 FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
				  WHERE j.machine_id=machine_record.id AND o.action=?
				    AND j.status IN ('queued','running'))`,
				int64(e.machineID), BulkActionReimage); err != nil {
				return 0, nil, err
			}
		}
	}
	// Mirror CompleteJob's terminal flip for ops whose last outstanding job
	// just expired.
	var completedOps []ID
	for _, opID := range opOrder {
		res, err := tx.ExecContext(ctx, `
			UPDATE bulk_operation SET status='completed'
			WHERE id=? AND status='active' AND schedule_kind!='recurring'
			  AND NOT EXISTS (
			    SELECT 1 FROM bulk_job
			    WHERE operation_id=bulk_operation.id AND status IN ('queued','running'))`,
			int64(opID))
		if err != nil {
			return 0, nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			completedOps = append(completedOps, opID)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return cancelled, completedOps, nil
}

// CancelOperation cancels every queued/running job (so agents stop claiming
// them), clears any pending re-image flags it set, and marks the operation
// cancelled.
func (r *BulkRepo) CancelOperation(ctx context.Context, id ID) error {
	op, _, err := r.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if op.Action == BulkActionReimage {
		if _, err := tx.ExecContext(ctx, `
			UPDATE machine_record SET reimage_pending=0, reimage_image_id=0
			WHERE id IN (SELECT machine_id FROM bulk_job
			             WHERE operation_id=? AND status IN ('queued','running'))`,
			int64(id)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bulk_job SET status='cancelled', completed_at=CURRENT_TIMESTAMP
		WHERE operation_id=? AND status IN ('queued','running')`, int64(id)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE bulk_operation SET status='cancelled', next_run_at=NULL WHERE id=?`,
		int64(id)); err != nil {
		return err
	}
	return tx.Commit()
}

// PauseOperation suspends a recurring operation (no further runs until resumed).
func (r *BulkRepo) PauseOperation(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE bulk_operation SET status='paused', next_run_at=NULL
		 WHERE id=? AND schedule_kind='recurring' AND status IN ('scheduled','active')`, int64(id))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: only an active recurring operation can be paused", ErrValidation)
	}
	return nil
}

// ResumeOperation re-arms a paused recurring operation from now.
func (r *BulkRepo) ResumeOperation(ctx context.Context, id ID) error {
	op, _, err := r.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.ScheduleKind != BulkScheduleRecurring || op.Status != BulkStatusPaused {
		return fmt.Errorf("%w: only a paused recurring operation can be resumed", ErrValidation)
	}
	rs, err := parseRecur(op.RecurSpec)
	if err != nil {
		return err
	}
	nr, err := nextRun(time.Now().In(r.location()), rs)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE bulk_operation SET status='active', next_run_at=? WHERE id=?`, nr.UTC(), int64(id))
	return err
}

// UpdateOperation edits a not-yet-running operation in place. It is rejected
// once a run is in flight (any queued/running jobs) so an operator can't change
// what an agent is about to do mid-flight.
func (r *BulkRepo) UpdateOperation(ctx context.Context, in BulkOperation) error {
	in = withScheduleDefaults(in)
	switch in.Action {
	case BulkActionRename, BulkActionSoftwarePush, BulkActionScript, BulkActionReimage:
	default:
		return fmt.Errorf("%w: unknown action %q", ErrValidation, in.Action)
	}
	if !json.Valid([]byte(in.Payload)) {
		return fmt.Errorf("%w: payload must be valid JSON", ErrValidation)
	}
	if err := validateSchedule(in); err != nil {
		return err
	}
	applyBulkNameDefaults(&in)
	// Store one-time times in UTC (see CreateOperation) so the driver reads
	// next_run_at/run_at back cleanly regardless of the configured zone.
	if in.RunAt != nil {
		u := in.RunAt.UTC()
		in.RunAt = &u
	}
	cur, _, err := r.GetOperation(ctx, in.ID)
	if err != nil {
		return err
	}
	var inflight int
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bulk_job WHERE operation_id=? AND status IN ('queued','running')`,
		int64(in.ID)).Scan(&inflight)
	if inflight > 0 || cur.Status == BulkStatusCancelled || cur.Status == BulkStatusCompleted {
		return fmt.Errorf("%w: this operation has already run and can't be edited", ErrValidation)
	}

	targetJSON, err := json.Marshal(in.Target)
	if err != nil {
		return err
	}
	status := BulkStatusScheduled
	var nextRunAt *time.Time
	if in.ScheduleKind == BulkScheduleNow {
		// Editing into a "run now" isn't supported (there'd be nothing to
		// schedule); keep it scheduled-immediate is out of scope.
		return fmt.Errorf("%w: choose a one-time or recurring schedule when editing", ErrValidation)
	}
	nr, err := firstRun(time.Now().In(r.location()), in)
	if err != nil {
		return err
	}
	nr = nr.UTC()
	nextRunAt = &nr

	_, err = r.db.ExecContext(ctx, `
		UPDATE bulk_operation
		SET action=?, payload=?, target_json=?, target_mode=?, schedule_kind=?,
		    run_at=?, recur_spec=?, next_run_at=?, reimage_image_id=?, status=?,
		    name=?, description=?, wake_on_lan=?, cancel_after_minutes=?
		WHERE id=?`,
		in.Action, in.Payload, string(targetJSON), in.TargetMode, in.ScheduleKind,
		in.RunAt, in.RecurSpec, nextRunAt, int64(in.ReimageImageID), status,
		in.Name, in.Description, in.WakeOnLAN, in.CancelAfterMinutes, int64(in.ID))
	return err
}

// ClearCompleted deletes every completed or cancelled operation (cascading to
// its jobs). Returns the number removed.
func (r *BulkRepo) ClearCompleted(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM bulk_operation WHERE status IN ('completed','cancelled')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- Minimal 5-field cron support (minute hour dom month dow) -------------
//
// Supports '*', lists (a,b), ranges (a-b), and steps (*/n, a-b/n). Day-of-month
// and day-of-week follow Vixie semantics: when both are restricted, a match on
// either selects the day. Used only on schedule advance, so the minute-by-minute
// scan (capped at ~400 days) is cheap.

type cronExpr struct {
	min, hour, dom, month, dow []bool
	domStar, dowStar           bool
}

func parseCron(expr string) (cronExpr, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cronExpr{}, fmt.Errorf("expected 5 fields, got %d", len(f))
	}
	var c cronExpr
	var err error
	if c.min, err = cronField(f[0], 0, 59); err != nil {
		return c, err
	}
	if c.hour, err = cronField(f[1], 0, 23); err != nil {
		return c, err
	}
	if c.dom, err = cronField(f[2], 1, 31); err != nil {
		return c, err
	}
	if c.month, err = cronField(f[3], 1, 12); err != nil {
		return c, err
	}
	if c.dow, err = cronField(f[4], 0, 6); err != nil {
		return c, err
	}
	c.domStar = f[2] == "*"
	c.dowStar = f[4] == "*"
	return c, nil
}

func cronField(field string, min, max int) ([]bool, error) {
	set := make([]bool, max+1)
	for _, part := range strings.Split(field, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s < 1 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			step = s
			part = part[:i]
		}
		lo, hi := min, max
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			ab := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("bad range %q", part)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", part)
			}
			lo, hi = v, v
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("value out of range in %q", field)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return set, nil
}

func cronNext(after time.Time, expr string) (time.Time, error) {
	c, err := parseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(0, 0, 400)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if c.matches(t) {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cron %q has no match within 400 days", expr)
}

func (c cronExpr) matches(t time.Time) bool {
	if !c.min[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	domOK := c.dom[t.Day()]
	dowOK := c.dow[int(t.Weekday())]
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

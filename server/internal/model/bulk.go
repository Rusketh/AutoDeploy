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

// BulkActionLabel is a short human label for an action token, used in default
// names/descriptions and the UI.
func BulkActionLabel(action string) string {
	switch action {
	case BulkActionRename:
		return "Rename"
	case BulkActionSoftwarePush:
		return "Install software"
	case BulkActionScript:
		return "Run script"
	case BulkActionReimage:
		return "Re-image"
	default:
		return action
	}
}

// bulkTargetSummary describes a target selection in a few words, for default
// names/descriptions: a machine count for an explicit basket, or the active
// name/OU/group filters for a re-evaluated target.
func bulkTargetSummary(t BulkTarget) string {
	if n := len(t.MachineIDs); n > 0 {
		if n == 1 {
			return "1 machine"
		}
		return fmt.Sprintf("%d machines", n)
	}
	var parts []string
	if t.NameRegex != "" {
		parts = append(parts, "name /"+t.NameRegex+"/")
	}
	if t.OU != "" {
		parts = append(parts, "OU "+t.OU)
	}
	if t.Group != "" {
		parts = append(parts, "group "+t.Group)
	}
	if len(parts) == 0 {
		return "no targets"
	}
	return strings.Join(parts, ", ")
}

// defaultBulkName builds a sortable fallback name from the action and target,
// e.g. "Re-image — 12 machines" or "Run script — group Lab-Computers".
func defaultBulkName(op BulkOperation) string {
	return BulkActionLabel(op.Action) + " — " + bulkTargetSummary(op.Target)
}

// defaultBulkDescription builds a fuller fallback description from the action,
// target and schedule, e.g. "Re-image on 12 machines, scheduled once."
func defaultBulkDescription(op BulkOperation) string {
	when := "run immediately"
	switch op.ScheduleKind {
	case BulkScheduleOnce:
		when = "scheduled once"
	case BulkScheduleRecurring:
		when = "recurring"
	}
	return fmt.Sprintf("%s on %s, %s.", BulkActionLabel(op.Action), bulkTargetSummary(op.Target), when)
}

// applyBulkNameDefaults fills Name/Description from the action and target when
// the operator left them blank, and trims whitespace otherwise. Shared by
// CreateOperation and UpdateOperation so both paths get the same defaults.
func applyBulkNameDefaults(op *BulkOperation) {
	op.Name = strings.TrimSpace(op.Name)
	if op.Name == "" {
		op.Name = defaultBulkName(*op)
	}
	op.Description = strings.TrimSpace(op.Description)
	if op.Description == "" {
		op.Description = defaultBulkDescription(*op)
	}
}

// Operation lifecycle status. An operation moves scheduled -> active as the
// scheduler materialises its first run; active -> completed once every job is
// terminal (non-recurring only); cancelled/paused are operator-driven.
const (
	BulkStatusScheduled = "scheduled"
	BulkStatusActive    = "active"
	BulkStatusPaused    = "paused"
	BulkStatusCompleted = "completed"
	BulkStatusCancelled = "cancelled"
)

// Schedule kinds. "now" materialises jobs immediately at create time (the
// historical behaviour); "once" fires at RunAt; "recurring" re-fires on the
// cadence described by RecurSpec.
const (
	BulkScheduleNow       = "now"
	BulkScheduleOnce      = "once"
	BulkScheduleRecurring = "recurring"
)

// Target modes for a recurring run. "selection" replays the frozen
// MachineIDs basket every run; "filter" re-resolves the name/OU/group filter
// each run so newly-matching machines are picked up (and removed ones drop).
const (
	BulkTargetSelection = "selection"
	BulkTargetFilter    = "filter"
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
	ID ID `json:"id"`
	// Name is the operator-facing label and Description is optional free text;
	// together they make scheduled tasks easy to scan and sort. When the
	// operator leaves them blank, CreateOperation/UpdateOperation fill sensible
	// defaults from the action and target (see defaultBulkName/Description).
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Action      string     `json:"action"`
	Payload     string     `json:"payload"`
	Target      BulkTarget `json:"target"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	// ReimageImageID is the image to deploy for a reimage operation; 0
	// means "use each machine's existing binding". Persisted on the row so
	// a scheduled/recurring reimage can re-apply the flag on each run.
	ReimageImageID ID `json:"reimage_image_id,omitempty"`

	// Lifecycle + scheduling (migration 0022). Zero values reproduce the
	// historical "run now, single run" behaviour.
	Status       string     `json:"status"`
	ScheduleKind string     `json:"schedule_kind"`
	TargetMode   string     `json:"target_mode"`
	RecurSpec    string     `json:"recur_spec,omitempty"`
	RunAt        *time.Time `json:"run_at,omitempty"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	RunCount     int        `json:"run_count"`

	// Progress is a derived (not persisted) per-status job rollup, filled
	// by ListOperationsWithProgress / ProgressFor for the portal.
	Progress BulkProgress `json:"progress"`
}

// BulkProgress is the per-status job rollup for an operation (latest run for
// recurring operations). Derived, never stored.
type BulkProgress struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	OK        int `json:"ok"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Done counts jobs in a terminal state (ok/failed/cancelled).
func (p BulkProgress) Done() int { return p.OK + p.Failed + p.Cancelled }

// Percent is the share of jobs that have reached a terminal state, 0-100.
func (p BulkProgress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return int(float64(p.Done()) / float64(p.Total) * 100)
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
	RunNo       int        `json:"run_no"`
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

// CreateOperation persists the operation and, for the "now" schedule kind,
// resolves the targets and queues one bulk_job per targeted machine. For the
// "once"/"recurring" kinds the row is stored as scheduled with a computed
// next_run_at and no jobs yet -- the scheduler materialises them when due.
func (r *BulkRepo) CreateOperation(ctx context.Context, op BulkOperation) (BulkOperation, []BulkJob, error) {
	op = withScheduleDefaults(op)
	switch op.Action {
	case BulkActionRename, BulkActionSoftwarePush, BulkActionScript, BulkActionReimage:
	default:
		return BulkOperation{}, nil, fmt.Errorf("%w: unknown action %q", ErrValidation, op.Action)
	}
	if !json.Valid([]byte(op.Payload)) {
		return BulkOperation{}, nil, fmt.Errorf("%w: payload must be valid JSON", ErrValidation)
	}
	if err := validateSchedule(op); err != nil {
		return BulkOperation{}, nil, err
	}
	applyBulkNameDefaults(&op)
	targetJSON, err := json.Marshal(op.Target)
	if err != nil {
		return BulkOperation{}, nil, err
	}

	now := time.Now()
	immediate := op.ScheduleKind == BulkScheduleNow
	status := BulkStatusScheduled
	runCount := 0
	var nextRun *time.Time
	var targets []MachineRecord
	if immediate {
		status = BulkStatusActive
		runCount = 1
		// Resolve targets BEFORE opening the transaction: the pool caps at
		// one connection, so the open tx would deadlock a second read.
		targets, err = r.PreviewTargets(ctx, op.Target)
		if err != nil {
			return BulkOperation{}, nil, err
		}
	} else {
		nr, err := firstRun(now, op)
		if err != nil {
			return BulkOperation{}, nil, err
		}
		nextRun = &nr
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO bulk_operation
		   (action, payload, target_json, created_by, status, schedule_kind,
		    target_mode, run_at, recur_spec, next_run_at, last_run_at, run_count,
		    reimage_image_id, name, description)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.Action, op.Payload, string(targetJSON), op.CreatedBy, status, op.ScheduleKind,
		op.TargetMode, op.RunAt, op.RecurSpec, nextRun, nil, runCount,
		int64(op.ReimageImageID), op.Name, op.Description)
	if err != nil {
		return BulkOperation{}, nil, err
	}
	opID, _ := res.LastInsertId()
	if immediate {
		if err := r.insertJobsTx(ctx, tx, ID(opID), op.Action, op.ReimageImageID, targets, 1); err != nil {
			return BulkOperation{}, nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE bulk_operation SET last_run_at=? WHERE id=?`, now, opID); err != nil {
			return BulkOperation{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BulkOperation{}, nil, err
	}
	return r.GetOperation(ctx, ID(opID))
}

// insertJobsTx queues one bulk_job per targeted machine for the given run, and
// (for reimage) flags each machine so the boot client auto-deploys on its next
// network boot. Shared by the immediate-create path and the scheduler.
func (r *BulkRepo) insertJobsTx(ctx context.Context, tx *sql.Tx, opID ID, action string, reimageImageID ID, targets []MachineRecord, runNo int) error {
	for _, m := range targets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bulk_job (operation_id, machine_id, run_no) VALUES (?, ?, ?)`,
			int64(opID), int64(m.ID), runNo); err != nil {
			return err
		}
		if action == BulkActionReimage {
			if _, err := tx.ExecContext(ctx,
				`UPDATE machine_record SET reimage_pending=1, reimage_image_id=? WHERE id=?`,
				int64(reimageImageID), int64(m.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// bulkOpColumns is the shared SELECT list for a bulk_operation row; keep it in
// sync with scanOperation.
const bulkOpColumns = `id, action, payload, target_json, created_by, created_at,
	status, schedule_kind, target_mode, run_at, recur_spec, next_run_at,
	last_run_at, run_count, reimage_image_id, name, description`

// scanOperation reads a bulk_operation row in bulkOpColumns order.
func scanOperation(s interface{ Scan(...any) error }) (BulkOperation, error) {
	var v BulkOperation
	var targetJSON string
	var runAt, nextRun, lastRun sql.NullTime
	var reimageID int64
	if err := s.Scan(&v.ID, &v.Action, &v.Payload, &targetJSON, &v.CreatedBy, &v.CreatedAt,
		&v.Status, &v.ScheduleKind, &v.TargetMode, &runAt, &v.RecurSpec, &nextRun,
		&lastRun, &v.RunCount, &reimageID, &v.Name, &v.Description); err != nil {
		return BulkOperation{}, err
	}
	_ = json.Unmarshal([]byte(targetJSON), &v.Target)
	v.ReimageImageID = ID(reimageID)
	if runAt.Valid {
		t := runAt.Time
		v.RunAt = &t
	}
	if nextRun.Valid {
		t := nextRun.Time
		v.NextRunAt = &t
	}
	if lastRun.Valid {
		t := lastRun.Time
		v.LastRunAt = &t
	}
	return v, nil
}

// GetOperation returns the operation row plus its jobs.
func (r *BulkRepo) GetOperation(ctx context.Context, id ID) (BulkOperation, []BulkJob, error) {
	v, err := scanOperation(r.db.QueryRowContext(ctx,
		`SELECT `+bulkOpColumns+` FROM bulk_operation WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BulkOperation{}, nil, fmt.Errorf("operation %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return BulkOperation{}, nil, err
	}
	jobs, err := r.jobsFor(ctx, id)
	return v, jobs, err
}

func (r *BulkRepo) jobsFor(ctx context.Context, opID ID) ([]BulkJob, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.id, j.operation_id, j.machine_id, o.action, o.payload,
		       j.status, j.result_json, j.run_no, j.queued_at, j.claimed_at, j.completed_at
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
			&j.Status, &j.ResultJSON, &j.RunNo, &j.QueuedAt, &claimed, &completed); err != nil {
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
		  AND o.status NOT IN ('cancelled','paused')
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
	if _, err := r.db.ExecContext(ctx,
		`UPDATE bulk_job SET status=?, result_json=?, completed_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, resultJSON, jobID); err != nil {
		return err
	}
	// Flip a non-recurring operation to 'completed' once this was its last
	// outstanding job. Keeps the list query simple (status is authoritative)
	// without a separate sweep. Recurring operations stay active so they can
	// fire again; scheduled/cancelled/paused are left untouched.
	_, err := r.db.ExecContext(ctx, `
		UPDATE bulk_operation SET status='completed'
		WHERE id=(SELECT operation_id FROM bulk_job WHERE id=?)
		  AND status='active' AND schedule_kind!='recurring'
		  AND NOT EXISTS (
		    SELECT 1 FROM bulk_job
		    WHERE operation_id=bulk_operation.id AND status IN ('queued','running'))`,
		jobID)
	return err
}

// DeleteJobsForMachine removes every bulk_job row for a machine. Called when
// the machine is (re)imaged: queued jobs would otherwise run against the
// freshly-built OS unexpectedly, and finished jobs' results describe an OS
// that no longer exists. The parent bulk_operation rows (the operator's
// fleet-wide intent) are left intact.
func (r *BulkRepo) DeleteJobsForMachine(ctx context.Context, machineID ID) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM bulk_job WHERE machine_id=?`, machineID)
	return err
}

// LatestReimageJob returns the most recent re-image job queued for a machine
// (any status), or nil when there is none. The portal uses its queued_at and
// status to tell an operator how long a pending re-image has been waiting and
// whether the agent has already claimed it ("running") or not yet ("queued").
func (r *BulkRepo) LatestReimageJob(ctx context.Context, machineID ID) (*BulkJob, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT j.id, j.operation_id, j.machine_id, o.action, o.payload,
		       j.status, j.queued_at
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE j.machine_id=? AND o.action=?
		ORDER BY j.id DESC
		LIMIT 1`, machineID, BulkActionReimage)
	var j BulkJob
	err := row.Scan(&j.ID, &j.OperationID, &j.MachineID, &j.Action, &j.Payload, &j.Status, &j.QueuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// CancelReimageJobsForMachine neutralises any not-yet-finished re-image job for
// a machine so it won't reboot the box after an operator cancels the pending
// re-image. Queued/running re-image jobs are marked failed with a cancel note
// (a still-queued job will never be claimed; a just-claimed one at least
// records the operator's intent). Returns the number of rows affected.
func (r *BulkRepo) CancelReimageJobsForMachine(ctx context.Context, machineID ID) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE bulk_job
		   SET status='failed',
		       result_json='{"cancelled":"re-image cancelled by operator"}',
		       completed_at=CURRENT_TIMESTAMP
		 WHERE machine_id=? AND status IN ('queued','running')
		   AND operation_id IN (SELECT id FROM bulk_operation WHERE action=?)`,
		machineID, BulkActionReimage)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListOperations returns operations newest first.
func (r *BulkRepo) ListOperations(ctx context.Context) ([]BulkOperation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+bulkOpColumns+` FROM bulk_operation ORDER BY created_at DESC`)
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

// ListOperationsWithProgress returns operations newest first, each with its
// latest-run job rollup filled in (BulkOperation.Progress). One grouped query
// keeps it cheap regardless of job count.
func (r *BulkRepo) ListOperationsWithProgress(ctx context.Context) ([]BulkOperation, error) {
	ops, err := r.ListOperations(ctx)
	if err != nil {
		return nil, err
	}
	// Aggregate the latest run's jobs per operation in one pass.
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.operation_id, j.status, COUNT(*)
		FROM bulk_job j
		JOIN (SELECT operation_id, MAX(run_no) AS run_no FROM bulk_job GROUP BY operation_id) m
		  ON m.operation_id=j.operation_id AND m.run_no=j.run_no
		GROUP BY j.operation_id, j.status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byOp := map[ID]*BulkProgress{}
	for rows.Next() {
		var opID ID
		var status string
		var n int
		if err := rows.Scan(&opID, &status, &n); err != nil {
			return nil, err
		}
		p := byOp[opID]
		if p == nil {
			p = &BulkProgress{}
			byOp[opID] = p
		}
		addStatusCount(p, status, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range ops {
		if p := byOp[ops[i].ID]; p != nil {
			ops[i].Progress = *p
		}
	}
	return ops, nil
}

// ProgressFor returns the latest-run job rollup for a single operation.
func (r *BulkRepo) ProgressFor(ctx context.Context, id ID) (BulkProgress, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM bulk_job
		WHERE operation_id=? AND run_no=(SELECT COALESCE(MAX(run_no),1) FROM bulk_job WHERE operation_id=?)
		GROUP BY status`, id, id)
	if err != nil {
		return BulkProgress{}, err
	}
	defer rows.Close()
	var p BulkProgress
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return BulkProgress{}, err
		}
		addStatusCount(&p, status, n)
	}
	return p, rows.Err()
}

func addStatusCount(p *BulkProgress, status string, n int) {
	switch status {
	case "queued":
		p.Queued += n
	case "running":
		p.Running += n
	case "ok":
		p.OK += n
	case "failed":
		p.Failed += n
	case "cancelled":
		p.Cancelled += n
	}
	p.Total += n
}

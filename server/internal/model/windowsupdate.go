package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// WindowsUpdate is a single KB patch or .msu update.
type WindowsUpdate struct {
	ID              ID        `json:"id"`
	KBNumber        string    `json:"kb_number"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	OSFilter        string    `json:"os_filter"`
	Severity        string    `json:"severity"`
	StoragePath     string    `json:"storage_path"`
	PayloadFilename string    `json:"payload_filename"`
	SizeBytes       int64     `json:"size_bytes"`
	SupersedesJSON  string    `json:"supersedes_json"`
	RebootAfter     bool      `json:"reboot_after"`
	AutoDeploy      bool      `json:"auto_deploy"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// MachineUpdateStatus tracks whether a specific KB is installed on a machine.
type MachineUpdateStatus struct {
	MachineID   ID         `json:"machine_id"`
	KBNumber    string     `json:"kb_number"`
	Status      string     `json:"status"`
	InstalledAt *time.Time `json:"installed_at,omitempty"`
	ReportedAt  time.Time  `json:"reported_at"`
}

// UpdateDeployment is a push of one or more updates to targeted machines.
type UpdateDeployment struct {
	ID        ID         `json:"id"`
	UpdateIDs []ID       `json:"update_ids"`
	Target    BulkTarget `json:"target"`
	OSFilter  string     `json:"os_filter,omitempty"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
}

// UpdateDeploymentJob is a per-machine job for an update deployment.
type UpdateDeploymentJob struct {
	ID           ID         `json:"id"`
	DeploymentID ID         `json:"deployment_id"`
	MachineID    ID         `json:"machine_id"`
	UpdateID     ID         `json:"update_id"`
	Status       string     `json:"status"`
	ResultJSON   string     `json:"result_json,omitempty"`
	QueuedAt     time.Time  `json:"queued_at"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// ComplianceSummary is per-update install status across the fleet.
type ComplianceSummary struct {
	Installed int `json:"installed"`
	Pending   int `json:"pending"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
	Total     int `json:"total"`
}

// WindowsUpdateRepo manages windows_update CRUD, deployment, and compliance.
type WindowsUpdateRepo struct {
	db        *storage.DB
	Inventory *InventoryRepo
}

// NewWindowsUpdateRepo creates a new WindowsUpdateRepo.
func NewWindowsUpdateRepo(db *storage.DB, inv *InventoryRepo) *WindowsUpdateRepo {
	return &WindowsUpdateRepo{db: db, Inventory: inv}
}

// Create inserts a new windows update.
func (r *WindowsUpdateRepo) Create(ctx context.Context, in WindowsUpdate) (WindowsUpdate, error) {
	if strings.TrimSpace(in.KBNumber) == "" {
		return WindowsUpdate{}, fmt.Errorf("%w: kb_number is required", ErrValidation)
	}
	if strings.TrimSpace(in.Title) == "" {
		return WindowsUpdate{}, fmt.Errorf("%w: title is required", ErrValidation)
	}
	if in.SupersedesJSON == "" {
		in.SupersedesJSON = "[]"
	}
	reboot := 0
	if in.RebootAfter {
		reboot = 1
	}
	auto := 0
	if in.AutoDeploy {
		auto = 1
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO windows_update (kb_number, title, description, os_filter, severity,
			storage_path, payload_filename, size_bytes, supersedes_json, reboot_after, auto_deploy)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.KBNumber, in.Title, in.Description, in.OSFilter, in.Severity,
		in.StoragePath, in.PayloadFilename, in.SizeBytes, in.SupersedesJSON, reboot, auto)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return WindowsUpdate{}, fmt.Errorf("%w: KB %s already exists", ErrConflict, in.KBNumber)
		}
		return WindowsUpdate{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

// Get returns a single update by ID.
func (r *WindowsUpdateRepo) Get(ctx context.Context, id ID) (WindowsUpdate, error) {
	var u WindowsUpdate
	var reboot, auto int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, kb_number, title, description, os_filter, severity,
			storage_path, payload_filename, size_bytes, supersedes_json,
			reboot_after, auto_deploy, created_at, updated_at
		FROM windows_update WHERE id = ?`, id).Scan(
		&u.ID, &u.KBNumber, &u.Title, &u.Description, &u.OSFilter, &u.Severity,
		&u.StoragePath, &u.PayloadFilename, &u.SizeBytes, &u.SupersedesJSON,
		&reboot, &auto, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return WindowsUpdate{}, ErrNotFound
	}
	u.RebootAfter = reboot != 0
	u.AutoDeploy = auto != 0
	return u, err
}

// List returns all updates ordered by creation time (newest first).
func (r *WindowsUpdateRepo) List(ctx context.Context) ([]WindowsUpdate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kb_number, title, description, os_filter, severity,
			storage_path, payload_filename, size_bytes, supersedes_json,
			reboot_after, auto_deploy, created_at, updated_at
		FROM windows_update ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WindowsUpdate
	for rows.Next() {
		var u WindowsUpdate
		var reboot, auto int
		if err := rows.Scan(&u.ID, &u.KBNumber, &u.Title, &u.Description,
			&u.OSFilter, &u.Severity, &u.StoragePath, &u.PayloadFilename,
			&u.SizeBytes, &u.SupersedesJSON, &reboot, &auto, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.RebootAfter = reboot != 0
		u.AutoDeploy = auto != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// Update modifies an existing update's metadata.
func (r *WindowsUpdateRepo) Update(ctx context.Context, in WindowsUpdate) error {
	if strings.TrimSpace(in.KBNumber) == "" {
		return fmt.Errorf("%w: kb_number is required", ErrValidation)
	}
	if in.SupersedesJSON == "" {
		in.SupersedesJSON = "[]"
	}
	reboot := 0
	if in.RebootAfter {
		reboot = 1
	}
	auto := 0
	if in.AutoDeploy {
		auto = 1
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE windows_update SET kb_number=?, title=?, description=?, os_filter=?,
			severity=?, supersedes_json=?, reboot_after=?, auto_deploy=?,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.KBNumber, in.Title, in.Description, in.OSFilter, in.Severity,
		in.SupersedesJSON, reboot, auto, in.ID)
	return err
}

// Delete removes an update and cascades to deployment jobs.
func (r *WindowsUpdateRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM windows_update WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPayload updates the storage path and file metadata after upload.
func (r *WindowsUpdateRepo) SetPayload(ctx context.Context, id ID, storagePath, filename string, sizeBytes int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE windows_update SET storage_path=?, payload_filename=?, size_bytes=?,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, storagePath, filename, sizeBytes, id)
	return err
}

// GetByKB returns the update with the given KB number (exact match).
func (r *WindowsUpdateRepo) GetByKB(ctx context.Context, kb string) (WindowsUpdate, error) {
	var id ID
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM windows_update WHERE kb_number = ?`, kb).Scan(&id)
	if err == sql.ErrNoRows {
		return WindowsUpdate{}, ErrNotFound
	}
	if err != nil {
		return WindowsUpdate{}, err
	}
	return r.Get(ctx, id)
}

// UpsertMachineStatuses bulk-upserts KB install status from an agent report.
func (r *WindowsUpdateRepo) UpsertMachineStatuses(ctx context.Context, machineID ID, statuses []MachineUpdateStatus) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO machine_update_status (machine_id, kb_number, status, installed_at, reported_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(machine_id, kb_number) DO UPDATE SET
			status=excluded.status, installed_at=excluded.installed_at, reported_at=CURRENT_TIMESTAMP`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range statuses {
		if _, err := stmt.ExecContext(ctx, machineID, s.KBNumber, s.Status, s.InstalledAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearMachineState removes a machine's Windows-update state: its per-KB
// compliance rows and its queued/finished update-deployment jobs. Called
// when the machine is (re)imaged — the new OS image starts at a clean patch
// baseline, so the old compliance and job history no longer describe it. The
// fleet-wide windows_update and update_deployment rows are left intact; the
// agent re-reports installed KBs after the new OS boots.
func (r *WindowsUpdateRepo) ClearMachineState(ctx context.Context, machineID ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM machine_update_status WHERE machine_id=?`, machineID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM update_deployment_job WHERE machine_id=?`, machineID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListMachineStatuses returns all KB statuses for a machine.
func (r *WindowsUpdateRepo) ListMachineStatuses(ctx context.Context, machineID ID) ([]MachineUpdateStatus, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT machine_id, kb_number, status, installed_at, reported_at
		FROM machine_update_status WHERE machine_id = ?
		ORDER BY kb_number`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineUpdateStatus
	for rows.Next() {
		var s MachineUpdateStatus
		if err := rows.Scan(&s.MachineID, &s.KBNumber, &s.Status, &s.InstalledAt, &s.ReportedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ComplianceForUpdate returns install/pending/failed/unknown counts.
func (r *WindowsUpdateRepo) ComplianceForUpdate(ctx context.Context, updateID ID) (ComplianceSummary, error) {
	u, err := r.Get(ctx, updateID)
	if err != nil {
		return ComplianceSummary{}, err
	}
	machines, err := r.matchMachines(ctx, BulkTarget{}, u.OSFilter)
	if err != nil {
		return ComplianceSummary{}, err
	}
	var s ComplianceSummary
	s.Total = len(machines)
	for _, m := range machines {
		var status sql.NullString
		_ = r.db.QueryRowContext(ctx, `
			SELECT status FROM machine_update_status
			WHERE machine_id = ? AND kb_number = ?`, m.ID, u.KBNumber).Scan(&status)
		switch status.String {
		case "installed":
			s.Installed++
		case "pending":
			s.Pending++
		case "failed":
			s.Failed++
		default:
			s.Unknown++
		}
	}
	return s, nil
}

// CreateDeployment creates a deployment and queues one job per targeted machine
// per update.
func (r *WindowsUpdateRepo) CreateDeployment(ctx context.Context, d UpdateDeployment) (UpdateDeployment, []UpdateDeploymentJob, error) {
	if len(d.UpdateIDs) == 0 {
		return UpdateDeployment{}, nil, fmt.Errorf("%w: at least one update required", ErrValidation)
	}
	machines, err := r.matchMachines(ctx, d.Target, d.OSFilter)
	if err != nil {
		return UpdateDeployment{}, nil, err
	}
	if len(machines) == 0 {
		return UpdateDeployment{}, nil, fmt.Errorf("%w: no machines match the target", ErrValidation)
	}

	updateIDsJSON, _ := json.Marshal(d.UpdateIDs)
	targetJSON, _ := json.Marshal(d.Target)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return UpdateDeployment{}, nil, err
	}
	defer tx.Rollback()

	// Resolve each update's KB up front: it validates the IDs and lets the
	// job loop mark targeted machines pending per KB.
	kbByUpdate := make(map[ID]string, len(d.UpdateIDs))
	for _, uid := range d.UpdateIDs {
		var kb string
		err := tx.QueryRowContext(ctx,
			`SELECT kb_number FROM windows_update WHERE id = ?`, uid).Scan(&kb)
		if err == sql.ErrNoRows {
			return UpdateDeployment{}, nil, fmt.Errorf("%w: update %d not found", ErrValidation, uid)
		}
		if err != nil {
			return UpdateDeployment{}, nil, err
		}
		kbByUpdate[uid] = kb
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO update_deployment (update_ids_json, target_json, os_filter, created_by)
		VALUES (?, ?, ?, ?)`, string(updateIDsJSON), string(targetJSON), d.OSFilter, d.CreatedBy)
	if err != nil {
		return UpdateDeployment{}, nil, err
	}
	depID, _ := res.LastInsertId()
	d.ID = ID(depID)

	var jobs []UpdateDeploymentJob
	for _, m := range machines {
		for _, uid := range d.UpdateIDs {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO update_deployment_job (deployment_id, machine_id, update_id, status)
				VALUES (?, ?, ?, 'queued')`, depID, m.ID, uid)
			if err != nil {
				return UpdateDeployment{}, nil, err
			}
			jobID, _ := res.LastInsertId()
			jobs = append(jobs, UpdateDeploymentJob{
				ID: ID(jobID), DeploymentID: d.ID, MachineID: m.ID, UpdateID: ID(uid), Status: "queued",
			})
			if err := markPendingTx(ctx, tx, m.ID, kbByUpdate[uid]); err != nil {
				return UpdateDeployment{}, nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return UpdateDeployment{}, nil, err
	}
	d.CreatedAt = time.Now()
	return d, jobs, nil
}

// markPendingTx marks a machine pending for a KB so compliance reflects
// queued work immediately. Never downgrades an existing 'installed' row
// (e.g. a redundant re-deploy).
func markPendingTx(ctx context.Context, tx *sql.Tx, machineID ID, kb string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO machine_update_status (machine_id, kb_number, status, reported_at)
		VALUES (?, ?, 'pending', CURRENT_TIMESTAMP)
		ON CONFLICT(machine_id, kb_number) DO UPDATE SET
			status='pending', reported_at=CURRENT_TIMESTAMP
		WHERE machine_update_status.status != 'installed'`, machineID, kb)
	return err
}

// Auto-deploy pacing: a (machine, update) pair is attempted at most
// autoDeployMaxAttempts times, with attempts spaced by the SQLite
// datetime modifier autoDeployRetrySpacing, then left alone until an
// operator intervenes (a manual deployment still queues regardless and
// a reimage wipes the history, starting fresh).
const (
	autoDeployMaxAttempts  = 3
	autoDeployRetrySpacing = "-6 hours"
)

// EnsureAutoDeployJobs queues install jobs for every auto-deploy update
// applicable to this machine (OS-filter match) that is not already
// installed, in flight, retry-capped, or attempted too recently. Called
// on each agent poll before jobs are claimed, so machines added or
// reimaged later receive flagged updates with no operator action. Cheap
// when nothing has auto-deploy enabled. Auto jobs hang off a per-update
// deployment row (created_by='auto-deploy') so the portal shows their
// progress like any manual rollout.
func (r *WindowsUpdateRepo) EnsureAutoDeployJobs(ctx context.Context, machineID ID) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kb_number, os_filter FROM windows_update
		WHERE auto_deploy = 1 AND storage_path != ''`)
	if err != nil {
		return err
	}
	type autoUpdate struct {
		id       ID
		kb       string
		osFilter string
	}
	var autos []autoUpdate
	for rows.Next() {
		var a autoUpdate
		if err := rows.Scan(&a.id, &a.kb, &a.osFilter); err != nil {
			rows.Close()
			return err
		}
		autos = append(autos, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(autos) == 0 {
		return err
	}

	// The machine's OS caption decides applicability (List doesn't load
	// hardware, Get does).
	m, err := r.Inventory.Get(ctx, machineID)
	if err != nil {
		return err
	}
	caption := ""
	if m.Hardware != nil {
		caption = m.Hardware.OSCaption
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range autos {
		if !osFilterMatches(caption, a.osFilter) {
			continue
		}
		var status sql.NullString
		_ = tx.QueryRowContext(ctx, `
			SELECT status FROM machine_update_status
			WHERE machine_id = ? AND kb_number = ?`, machineID, a.kb).Scan(&status)
		if status.String == "installed" {
			continue
		}
		var attempts, active, recent int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN status IN ('queued','running') THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN completed_at >= datetime('now', ?) THEN 1 ELSE 0 END), 0)
			FROM update_deployment_job
			WHERE machine_id = ? AND update_id = ?`,
			autoDeployRetrySpacing, machineID, a.id).Scan(&attempts, &active, &recent); err != nil {
			return err
		}
		if active > 0 || attempts >= autoDeployMaxAttempts || recent > 0 {
			continue
		}
		depID, err := autoDeploymentTx(ctx, tx, a.id, a.osFilter)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO update_deployment_job (deployment_id, machine_id, update_id, status)
			VALUES (?, ?, ?, 'queued')`, depID, machineID, a.id); err != nil {
			return err
		}
		if err := markPendingTx(ctx, tx, machineID, a.kb); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// autoDeploymentTx returns the persistent auto-deploy deployment row for
// an update, creating it on first use. Grouping every auto-queued job
// under one deployment per update keeps the rollout observable on the
// deployment detail page.
func autoDeploymentTx(ctx context.Context, tx *sql.Tx, updateID ID, osFilter string) (int64, error) {
	idsJSON := fmt.Sprintf("[%d]", updateID)
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM update_deployment
		WHERE created_by = 'auto-deploy' AND update_ids_json = ?`, idsJSON).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO update_deployment (update_ids_json, target_json, os_filter, created_by)
		VALUES (?, '{}', ?, 'auto-deploy')`, idsJSON, osFilter)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListDeployments returns all update deployments (newest first).
func (r *WindowsUpdateRepo) ListDeployments(ctx context.Context) ([]UpdateDeployment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, update_ids_json, target_json, os_filter, created_by, created_at
		FROM update_deployment ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpdateDeployment
	for rows.Next() {
		var d UpdateDeployment
		var uidsJSON, targetJSON string
		if err := rows.Scan(&d.ID, &uidsJSON, &targetJSON, &d.OSFilter, &d.CreatedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(uidsJSON), &d.UpdateIDs)
		json.Unmarshal([]byte(targetJSON), &d.Target)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDeployment returns a deployment with its jobs.
func (r *WindowsUpdateRepo) GetDeployment(ctx context.Context, id ID) (UpdateDeployment, []UpdateDeploymentJob, error) {
	var d UpdateDeployment
	var uidsJSON, targetJSON string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, update_ids_json, target_json, os_filter, created_by, created_at
		FROM update_deployment WHERE id = ?`, id).Scan(
		&d.ID, &uidsJSON, &targetJSON, &d.OSFilter, &d.CreatedBy, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return UpdateDeployment{}, nil, ErrNotFound
	}
	if err != nil {
		return UpdateDeployment{}, nil, err
	}
	json.Unmarshal([]byte(uidsJSON), &d.UpdateIDs)
	json.Unmarshal([]byte(targetJSON), &d.Target)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, deployment_id, machine_id, update_id, status, result_json,
			queued_at, claimed_at, completed_at
		FROM update_deployment_job WHERE deployment_id = ?
		ORDER BY id`, id)
	if err != nil {
		return d, nil, err
	}
	defer rows.Close()
	var jobs []UpdateDeploymentJob
	for rows.Next() {
		var j UpdateDeploymentJob
		if err := rows.Scan(&j.ID, &j.DeploymentID, &j.MachineID, &j.UpdateID,
			&j.Status, &j.ResultJSON, &j.QueuedAt, &j.ClaimedAt, &j.CompletedAt); err != nil {
			return d, nil, err
		}
		jobs = append(jobs, j)
	}
	return d, jobs, rows.Err()
}

// ClaimUpdateJobs claims up to max queued update jobs for a machine.
//
// Before claiming it requeues this machine's jobs that have sat in
// 'running' for over two hours: a claim only proves the server handed
// the job out, not that the agent acted on it. An agent that crashed,
// rebooted mid-install, or is too old to understand update jobs would
// otherwise strand them in 'running' forever with no retry.
func (r *WindowsUpdateRepo) ClaimUpdateJobs(ctx context.Context, machineID ID, max int) ([]UpdateDeploymentJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE update_deployment_job
		SET status='queued', claimed_at=NULL
		WHERE machine_id = ? AND status = 'running'
		  AND claimed_at IS NOT NULL
		  AND claimed_at < datetime('now', '-2 hours')`, machineID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, deployment_id, update_id
		FROM update_deployment_job
		WHERE machine_id = ? AND status = 'queued'
		ORDER BY id LIMIT ?`, machineID, max)
	if err != nil {
		return nil, err
	}
	var jobs []UpdateDeploymentJob
	for rows.Next() {
		var j UpdateDeploymentJob
		j.MachineID = machineID
		j.Status = "running"
		if err := rows.Scan(&j.ID, &j.DeploymentID, &j.UpdateID); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()

	for i := range jobs {
		res, err := tx.ExecContext(ctx, `
			UPDATE update_deployment_job SET status='running', claimed_at=CURRENT_TIMESTAMP
			WHERE id = ? AND status = 'queued'`, jobs[i].ID)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			jobs[i].Status = "queued"
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	claimed := make([]UpdateDeploymentJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Status == "running" {
			claimed = append(claimed, j)
		}
	}
	return claimed, nil
}

// GetUpdateJob returns a single deployment job by ID.
func (r *WindowsUpdateRepo) GetUpdateJob(ctx context.Context, id ID) (UpdateDeploymentJob, error) {
	var j UpdateDeploymentJob
	err := r.db.QueryRowContext(ctx, `
		SELECT id, deployment_id, machine_id, update_id, status, result_json,
			queued_at, claimed_at, completed_at
		FROM update_deployment_job WHERE id = ?`, id).Scan(
		&j.ID, &j.DeploymentID, &j.MachineID, &j.UpdateID,
		&j.Status, &j.ResultJSON, &j.QueuedAt, &j.ClaimedAt, &j.CompletedAt)
	if err == sql.ErrNoRows {
		return UpdateDeploymentJob{}, ErrNotFound
	}
	return j, err
}

// CompleteUpdateJob marks a job as completed with a final status.
func (r *WindowsUpdateRepo) CompleteUpdateJob(ctx context.Context, jobID ID, status, resultJSON string) error {
	if status != "ok" && status != "failed" {
		return fmt.Errorf("%w: status must be ok or failed", ErrValidation)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE update_deployment_job SET status=?, result_json=?, completed_at=CURRENT_TIMESTAMP
		WHERE id=?`, status, resultJSON, jobID)
	return err
}

// PreviewTargets resolves a target+OS filter without creating anything.
func (r *WindowsUpdateRepo) PreviewTargets(ctx context.Context, t BulkTarget, osFilter string) ([]MachineRecord, error) {
	return r.matchMachines(ctx, t, osFilter)
}

// FleetUpdateSummary holds fleet-wide Windows Update compliance stats.
type FleetUpdateSummary struct {
	TotalUpdates       int
	UpdatesWithPayload int
	TotalMachines      int
	FullyPatched       int
	WithPending        int
}

// FleetUpdateSummary computes fleet-wide Windows Update compliance.
func (r *WindowsUpdateRepo) FleetUpdateSummary(ctx context.Context) (FleetUpdateSummary, error) {
	var s FleetUpdateSummary
	updates, err := r.List(ctx)
	if err != nil {
		return s, err
	}
	s.TotalUpdates = len(updates)
	for _, u := range updates {
		if u.StoragePath != "" {
			s.UpdatesWithPayload++
		}
	}
	machines, err := r.Inventory.List(ctx)
	if err != nil {
		return s, err
	}
	s.TotalMachines = len(machines)
	if s.TotalUpdates == 0 || s.TotalMachines == 0 {
		return s, nil
	}
	kbs := make([]string, len(updates))
	for i, u := range updates {
		kbs[i] = u.KBNumber
	}
	for _, m := range machines {
		statuses, _ := r.ListMachineStatuses(ctx, m.ID)
		statusMap := map[string]string{}
		for _, st := range statuses {
			statusMap[st.KBNumber] = st.Status
		}
		allInstalled := true
		hasPending := false
		for _, kb := range kbs {
			switch statusMap[kb] {
			case "installed":
			case "pending":
				hasPending = true
				allInstalled = false
			default:
				allInstalled = false
			}
		}
		if allInstalled {
			s.FullyPatched++
		}
		if hasPending {
			s.WithPending++
		}
	}
	return s, nil
}

// AllComplianceSummaries returns compliance stats for every tracked update
// in a single batch. Used by the update list page to show inline compliance.
func (r *WindowsUpdateRepo) AllComplianceSummaries(ctx context.Context) (map[ID]ComplianceSummary, error) {
	updates, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[ID]ComplianceSummary, len(updates))
	for _, u := range updates {
		c, err := r.ComplianceForUpdate(ctx, u.ID)
		if err != nil {
			continue
		}
		out[u.ID] = c
	}
	return out, nil
}

// osFilterMatches reports whether a machine's reported OS caption
// satisfies a portal OS filter. Filters are dash-style ("windows-10",
// "server-2022") while WMI captions read "Microsoft Windows 10 Pro", so
// the filter is normalized to spaces for the substring match. An empty
// filter matches every machine; an empty caption (no reported hardware,
// so an unknown OS) never matches an explicit filter.
func osFilterMatches(caption, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	filter = strings.ReplaceAll(filter, "-", " ")
	if filter == "" {
		return true
	}
	if caption == "" {
		return false
	}
	return strings.Contains(strings.ToLower(caption), filter)
}

// matchMachines resolves a BulkTarget + OS filter to matching machines.
func (r *WindowsUpdateRepo) matchMachines(ctx context.Context, t BulkTarget, osFilter string) ([]MachineRecord, error) {
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
	idSet := map[ID]bool{}
	for _, id := range t.MachineIDs {
		idSet[id] = true
	}
	out := make([]MachineRecord, 0, len(all))
	for _, m := range all {
		if len(idSet) > 0 && !idSet[m.ID] {
			continue
		}
		if strings.TrimSpace(osFilter) != "" {
			// List doesn't load hardware_json; fetch the full record for
			// the caption. A machine with no reported hardware has an
			// unknown OS and never matches an explicit filter.
			full := m
			if full.Hardware == nil {
				var gerr error
				full, gerr = r.Inventory.Get(ctx, m.ID)
				if gerr != nil {
					continue
				}
			}
			caption := ""
			if full.Hardware != nil {
				caption = full.Hardware.OSCaption
			}
			if !osFilterMatches(caption, osFilter) {
				continue
			}
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

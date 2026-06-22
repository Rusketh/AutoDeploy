package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// MachineRecord is a known machine in inventory, keyed on its stable
// hardware identity (SMBIOS UUID).
type MachineRecord struct {
	ID         ID     `json:"id"`
	SystemUUID string `json:"system_uuid"`
	// AgentID is AutoDeploy's own per-machine UUID, minted server-side
	// (migration 0012). It is the stable object id the agent stores in the
	// registry and polls with -- the BIOS UUID is not trusted for
	// uniqueness across a bulk fleet.
	AgentID            string `json:"agent_id"`
	SystemSerial       string `json:"system_serial"`
	SystemManufacturer string `json:"system_manufacturer"`
	SystemProduct      string `json:"system_product"`
	BIOSVendor         string `json:"bios_vendor"`
	BIOSVersion        string `json:"bios_version"`
	BoardManufacturer  string `json:"board_manufacturer"`
	BoardProduct       string `json:"board_product"`
	BoardSerial        string `json:"board_serial"`
	// ReportedName / ADDistinguishedName are OBSERVED facts the running agent
	// reports each poll: the machine's current Windows computer name and its
	// full AD distinguished name. They track reality (a manual rename, an AD
	// move) and drive the inventory's Name + AD-location display; the binding
	// holds the DESIRED name/OU and is synced from these. Empty until the
	// agent reports (migration 0017).
	ReportedName        string `json:"reported_name"`
	ADDistinguishedName string `json:"ad_distinguished_name"`
	// Hardware is the full spec set the agent collects (CPU/RAM/disks/
	// GPU/NICs), reported on each poll. Empty until the agent reports.
	Hardware  *Hardware `json:"hardware,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// FirstContact is set (derived, never persisted) when UpsertFromIdentity
	// just created this record — i.e. the machine was seen for the very first
	// time. Handlers use it to emit the machine.first_seen notification.
	FirstContact bool `json:"-"`
}

// Hardware is the machine's reported hardware inventory. The agent
// collects it (WMI/CIM on Windows) and posts it; the server stores it as
// a JSON blob in machine_record.hardware_json (migration 0014). Fields are
// best-effort -- whatever the agent could read.
type Hardware struct {
	CPUModel    string         `json:"cpu_model,omitempty"`
	CPUCores    int            `json:"cpu_cores,omitempty"`
	CPUThreads  int            `json:"cpu_threads,omitempty"`
	MemoryBytes int64          `json:"memory_bytes,omitempty"`
	Disks       []HardwareDisk `json:"disks,omitempty"`
	GPUs        []string       `json:"gpus,omitempty"`
	NICs        []HardwareNIC  `json:"nics,omitempty"`
	OSCaption   string         `json:"os_caption,omitempty"`
	OSVersion   string         `json:"os_version,omitempty"`
	ReportedAt  time.Time      `json:"reported_at,omitempty"`
}

// HardwareDisk is one physical disk.
type HardwareDisk struct {
	Model     string `json:"model,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// HardwareNIC is one network adapter.
type HardwareNIC struct {
	Name string   `json:"name,omitempty"`
	MAC  string   `json:"mac,omitempty"`
	IPs  []string `json:"ips,omitempty"`
}

// MachineBinding is what the machine is assigned to: its image, AD
// placement, and group memberships. Software assignment is derived from
// the image (loadout + direct links) and from drift state.
type MachineBinding struct {
	MachineID        ID        `json:"machine_id"`
	ImageID          *ID       `json:"image_id,omitempty"`
	MachineName      string    `json:"machine_name"`
	TargetOU         string    `json:"target_ou"`
	GroupMemberships []string  `json:"group_memberships"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DeploymentRecord is one dated row in the audit log.
type DeploymentRecord struct {
	ID          ID         `json:"id"`
	MachineID   ID         `json:"machine_id"`
	ImageID     *ID        `json:"image_id,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Outcome     string     `json:"outcome"` // in_progress | ok | failed
	Notes       string     `json:"notes,omitempty"`
	// Phase is the canonical milestone token for an in_progress deploy, used
	// to drive the live progress bar:
	//   staging | staged | specialize | first-logon | agent-online |
	//   installing | complete
	// Empty on legacy rows written before the column existed (rendered as an
	// indeterminate bar while still in_progress).
	Phase string `json:"phase,omitempty"`
	// ProgressDone / ProgressTotal track the agent-run software phase: how many
	// of the machine's assigned packages have been installed so far, out of the
	// total it set out to install. Both 0 until the agent reports the
	// "installing" phase. They scale the bar and render "(done/total)".
	ProgressDone  int `json:"progress_done,omitempty"`
	ProgressTotal int `json:"progress_total,omitempty"`
}

// DeployPhaseProgress maps a deployment's phase + outcome to a human label and
// a percent for the progress bar. indeterminate is true for legacy in_progress
// rows with no recorded phase, where the caller should render a value-less
// <progress> rather than 0%.
//
// For the agent-run "installing" phase the per-package fraction is not known
// here (it lives on the DeploymentRecord); callers with the record should use
// DeployRecordProgress, which overlays "(done/total)" and scales the bar. This
// function is the shared base both that and the static renders build on.
func DeployPhaseProgress(phase, outcome string) (label string, percent int, indeterminate bool) {
	switch outcome {
	case "ok":
		return "Done", 100, false
	case "failed":
		return "Failed", 100, false
	}
	// outcome is in_progress (or unknown) from here.
	switch phase {
	case "staging":
		return "Staging media", 10, false
	case "staged":
		return "Rebooting into Setup", 30, false
	case "specialize":
		return "Windows Setup (specialize)", 55, false
	case "first-logon":
		return "First logon (OOBE)", 65, false
	case "agent-online":
		return "Windows installed — agent running", 70, false
	case "installing":
		// No counts here: the band's low end. DeployRecordProgress scales it.
		return "Installing software", installBandLo, false
	case "complete":
		return "Finishing up", 95, false
	default:
		return "In progress", 0, true
	}
}

// installBandLo/installBandHi bound the progress-bar percent for the agent-run
// software-install phase: 0/total sits at Lo, total/total at Hi, just below
// the "complete" (95%) finishing step.
const (
	installBandLo = 75
	installBandHi = 92
)

// DeployRecordProgress is DeployPhaseProgress plus the software-install overlay:
// for an in_progress "installing" row with a known package total it renders
// "Installing software (done/total)" and scales the bar across the install band
// by the fraction installed. For every other phase it is exactly
// DeployPhaseProgress. It is the single source of truth for the machine detail
// page render and the live deploy-status endpoint.
func DeployRecordProgress(d DeploymentRecord) (label string, percent int, indeterminate bool) {
	label, percent, indeterminate = DeployPhaseProgress(d.Phase, d.Outcome)
	if d.Outcome == "in_progress" && d.Phase == "installing" && d.ProgressTotal > 0 {
		done := d.ProgressDone
		if done < 0 {
			done = 0
		}
		if done > d.ProgressTotal {
			done = d.ProgressTotal
		}
		percent = installBandLo + (installBandHi-installBandLo)*done/d.ProgressTotal
		label = fmt.Sprintf("Installing software (%d/%d)", done, d.ProgressTotal)
	}
	return label, percent, indeterminate
}

// DetectedState is one package's last-known detection result for a machine.
type DetectedState struct {
	MachineID         ID        `json:"machine_id"`
	SoftwarePackageID ID        `json:"software_package_id"`
	Detected          bool      `json:"detected"`
	LastEvaluatedAt   time.Time `json:"last_evaluated_at"`
}

// InventoryRepo owns machine_record, machine_binding, deployment_history
// and machine_detected_state.
type InventoryRepo struct{ db *storage.DB }

func NewInventoryRepo(db *storage.DB) *InventoryRepo { return &InventoryRepo{db: db} }

// UpsertFromIdentity creates a machine_record if one does not exist for
// id.SystemUUID, or updates its last_seen and identity fields if it does.
// Returns the resulting record.
func (r *InventoryRepo) UpsertFromIdentity(ctx context.Context, id match.Identity) (MachineRecord, error) {
	if strings.TrimSpace(id.SystemUUID) == "" {
		return MachineRecord{}, fmt.Errorf("%w: system_uuid required", ErrValidation)
	}
	existing, err := r.GetByUUID(ctx, id.SystemUUID)
	if err == nil {
		// Update identity fields and last_seen. Only non-empty incoming
		// values overwrite what's stored: the SMBIOS make/model are read
		// pre-boot by the Boot Client, but the resident agent (and several
		// other callers) report an identity carrying only the UUID. Without
		// COALESCE those empty fields would wipe the make/model on the very
		// next agent check-in. A real SMBIOS report (e.g. after a firmware
		// update that changes vendor strings) still updates them, because
		// those values are non-empty. Backfill agent_id if this is a row that
		// predates migration 0012 (still empty); never overwrite an id
		// already minted.
		_, err := r.db.ExecContext(ctx, `
			UPDATE machine_record
			SET system_serial       = COALESCE(NULLIF(?, ''), system_serial),
			    system_manufacturer = COALESCE(NULLIF(?, ''), system_manufacturer),
			    system_product      = COALESCE(NULLIF(?, ''), system_product),
			    bios_vendor         = COALESCE(NULLIF(?, ''), bios_vendor),
			    bios_version        = COALESCE(NULLIF(?, ''), bios_version),
			    board_manufacturer  = COALESCE(NULLIF(?, ''), board_manufacturer),
			    board_product       = COALESCE(NULLIF(?, ''), board_product),
			    board_serial        = COALESCE(NULLIF(?, ''), board_serial),
			    agent_id=CASE WHEN agent_id='' THEN ? ELSE agent_id END,
			    last_seen=CURRENT_TIMESTAMP,
			    offline_notified_at=NULL
			WHERE id=?`,
			id.SystemSerial, id.SystemManufacturer, id.SystemProduct,
			id.BIOSVendor, id.BIOSVersion,
			id.BoardManufacturer, id.BoardProduct, id.BoardSerial,
			uuid.NewString(),
			existing.ID)
		if err != nil {
			return MachineRecord{}, err
		}
		return r.Get(ctx, existing.ID)
	}
	if !errors.Is(err, ErrNotFound) {
		return MachineRecord{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO machine_record
		    (system_uuid, agent_id, system_serial, system_manufacturer, system_product,
		     bios_vendor, bios_version, board_manufacturer, board_product, board_serial)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id.SystemUUID, uuid.NewString(), id.SystemSerial, id.SystemManufacturer, id.SystemProduct,
		id.BIOSVendor, id.BIOSVersion,
		id.BoardManufacturer, id.BoardProduct, id.BoardSerial)
	if err != nil {
		return MachineRecord{}, err
	}
	newID, _ := res.LastInsertId()
	m, err := r.Get(ctx, ID(newID))
	m.FirstContact = err == nil
	return m, err
}

// Get returns the machine record by primary key.
func (r *InventoryRepo) Get(ctx context.Context, id ID) (MachineRecord, error) {
	var v MachineRecord
	var hw sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, system_uuid, agent_id, system_serial, system_manufacturer, system_product,
		       bios_vendor, bios_version, board_manufacturer, board_product, board_serial,
		       hardware_json, reported_name, ad_dn, first_seen, last_seen
		FROM machine_record WHERE id=?`, id).Scan(
		&v.ID, &v.SystemUUID, &v.AgentID, &v.SystemSerial, &v.SystemManufacturer, &v.SystemProduct,
		&v.BIOSVendor, &v.BIOSVersion, &v.BoardManufacturer, &v.BoardProduct, &v.BoardSerial,
		&hw, &v.ReportedName, &v.ADDistinguishedName, &v.FirstSeen, &v.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRecord{}, fmt.Errorf("machine %d: %w", id, ErrNotFound)
	}
	v.Hardware = parseHardware(hw.String)
	return v, err
}

// parseHardware decodes a hardware_json blob; empty/bad JSON yields nil so
// a machine that hasn't reported (or reported garbage) just shows no specs.
func parseHardware(s string) *Hardware {
	if s == "" {
		return nil
	}
	var h Hardware
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return nil
	}
	return &h
}

// UpdateHardware stores the agent-reported hardware spec for a machine.
func (r *InventoryRepo) UpdateHardware(ctx context.Context, machineID ID, h Hardware) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE machine_record SET hardware_json=? WHERE id=?`, string(b), machineID)
	return err
}

// ListOSCaptions returns every machine's reported OS caption in one query,
// keyed by machine ID. Machines with no hardware report are absent. The
// machines list needs each visible row's OS without paying a per-machine
// Get (List deliberately skips hardware_json).
func (r *InventoryRepo) ListOSCaptions(ctx context.Context) (map[ID]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, hardware_json FROM machine_record
		WHERE hardware_json IS NOT NULL AND hardware_json != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]string{}
	for rows.Next() {
		var id ID
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		// Decode only the one field we need; garbage JSON just yields "".
		var hw struct {
			OSCaption string `json:"os_caption"`
		}
		if json.Unmarshal([]byte(raw), &hw) == nil && hw.OSCaption != "" {
			out[id] = hw.OSCaption
		}
	}
	return out, rows.Err()
}

// UpdateObservedIdentity stores the running agent's reported current computer
// name and AD distinguished name, and refreshes last_seen. Keyed by the
// server-minted agent_id. Returns the machine so the caller can sync the
// binding from the observed values.
func (r *InventoryRepo) UpdateObservedIdentity(ctx context.Context, agentID, name, dn string) (MachineRecord, error) {
	m, err := r.GetByAgentID(ctx, agentID)
	if err != nil {
		return MachineRecord{}, err
	}
	if _, err := r.db.ExecContext(ctx, `
		UPDATE machine_record
		SET reported_name=?, ad_dn=?, last_seen=CURRENT_TIMESTAMP
		WHERE id=?`, name, dn, m.ID); err != nil {
		return MachineRecord{}, err
	}
	m.ReportedName, m.ADDistinguishedName = name, dn
	return m, nil
}

// SyncBindingFromObserved updates a machine's binding to reflect reality the
// agent reported: a manual rename updates MachineName, an AD move updates
// TargetOU. A MachineName that is an operator-set TEMPLATE (contains '%') is
// left untouched so re-image keeps expanding it per machine. Empty values are
// ignored, equal values are no-ops, and a binding is created if none exists.
func (r *InventoryRepo) SyncBindingFromObserved(ctx context.Context, machineID ID, name, ou string) error {
	b, err := r.GetBinding(ctx, machineID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		b = MachineBinding{MachineID: machineID}
	}
	changed := false
	if name != "" && !strings.Contains(b.MachineName, "%") && b.MachineName != name {
		b.MachineName = name
		changed = true
	}
	if ou != "" && b.TargetOU != ou {
		b.TargetOU = ou
		changed = true
	}
	if !changed {
		return nil
	}
	return r.UpsertBinding(ctx, b)
}

// TouchLastSeen bumps a machine's last_seen to now. Called on every resident
// agent /self poll so "last seen" reflects real check-in liveness, not just
// the last boot-client/identity report. Best-effort at the call site. Also
// closes any open offline episode so the next one notifies again.
func (r *InventoryRepo) TouchLastSeen(ctx context.Context, id ID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE machine_record SET last_seen=CURRENT_TIMESTAMP, offline_notified_at=NULL WHERE id=?`, id)
	return err
}

// ListOfflineUnnotified returns machines that have not been seen for at
// least thresholdMinutes and have no machine.offline notification recorded
// for the current episode. The comparison runs entirely in SQLite
// (last_seen is CURRENT_TIMESTAMP-written) so no Go/DB time-format mixing.
func (r *InventoryRepo) ListOfflineUnnotified(ctx context.Context, thresholdMinutes int) ([]MachineRecord, error) {
	if thresholdMinutes <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, system_uuid, agent_id, reported_name, first_seen, last_seen
		FROM machine_record
		WHERE last_seen < datetime('now', '-' || ? || ' minutes')
		  AND offline_notified_at IS NULL
		ORDER BY id`, thresholdMinutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRecord
	for rows.Next() {
		var v MachineRecord
		if err := rows.Scan(&v.ID, &v.SystemUUID, &v.AgentID, &v.ReportedName,
			&v.FirstSeen, &v.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkOfflineNotified records that a machine.offline notification went out
// for these machines' current offline episode (cleared on next contact).
func (r *InventoryRepo) MarkOfflineNotified(ctx context.Context, ids []ID) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = int64(id)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE machine_record SET offline_notified_at=CURRENT_TIMESTAMP
		 WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

// GetByUUID looks up a machine by SMBIOS UUID.
func (r *InventoryRepo) GetByUUID(ctx context.Context, uuid string) (MachineRecord, error) {
	var v MachineRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT id, system_uuid, agent_id, system_serial, system_manufacturer, system_product,
		       bios_vendor, bios_version, board_manufacturer, board_product, board_serial,
		       first_seen, last_seen
		FROM machine_record WHERE system_uuid=?`, uuid).Scan(
		&v.ID, &v.SystemUUID, &v.AgentID, &v.SystemSerial, &v.SystemManufacturer, &v.SystemProduct,
		&v.BIOSVendor, &v.BIOSVersion, &v.BoardManufacturer, &v.BoardProduct, &v.BoardSerial,
		&v.FirstSeen, &v.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRecord{}, fmt.Errorf("machine uuid %s: %w", uuid, ErrNotFound)
	}
	return v, err
}

// SetReimagePending flags a machine to re-image on its next network boot.
// imageID 0 means "use the machine's existing binding". The running agent
// reboots the machine; the boot client auto-deploys the flagged image.
func (r *InventoryRepo) SetReimagePending(ctx context.Context, machineID, imageID ID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE machine_record SET reimage_pending=1, reimage_image_id=? WHERE id=?`,
		int64(imageID), machineID)
	return err
}

// ClearReimagePending clears the flag. Called when the boot client reports
// the deploy has started, so the re-image fires exactly once.
func (r *InventoryRepo) ClearReimagePending(ctx context.Context, machineID ID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE machine_record SET reimage_pending=0, reimage_image_id=0 WHERE id=?`,
		machineID)
	return err
}

// ClearDetectedState deletes the machine's per-package software detection
// results. Called when a (re)deploy is staged: the OS is being rebuilt, so
// the last-known detection state from the previous install is stale. The
// resident agent re-evaluates and re-reports after the new OS boots.
func (r *InventoryRepo) ClearDetectedState(ctx context.Context, machineID ID) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM machine_detected_state WHERE machine_id=?`, machineID)
	return err
}

// ReimagePending reports whether the machine (by UUID) is flagged, and the
// image to deploy (0 => use its binding). Used by the boot menu to decide
// whether to auto-deploy.
func (r *InventoryRepo) ReimagePending(ctx context.Context, uuid string) (pending bool, imageID ID, err error) {
	var p int
	var img int64
	e := r.db.QueryRowContext(ctx,
		`SELECT reimage_pending, reimage_image_id FROM machine_record WHERE system_uuid=?`,
		uuid).Scan(&p, &img)
	if errors.Is(e, sql.ErrNoRows) {
		return false, 0, nil
	}
	if e != nil {
		return false, 0, e
	}
	return p != 0, ID(img), nil
}

// ReimagePendingByID reports the re-image flag and target image (0 => use the
// machine's binding) for a machine looked up by id. The portal machine page
// uses it to show a "re-image pending" indicator so an operator can see a
// queued re-image is waiting for the agent to reboot the box.
func (r *InventoryRepo) ReimagePendingByID(ctx context.Context, machineID ID) (pending bool, imageID ID, err error) {
	var p int
	var img int64
	e := r.db.QueryRowContext(ctx,
		`SELECT reimage_pending, reimage_image_id FROM machine_record WHERE id=?`,
		machineID).Scan(&p, &img)
	if errors.Is(e, sql.ErrNoRows) {
		return false, 0, nil
	}
	if e != nil {
		return false, 0, e
	}
	return p != 0, ID(img), nil
}

// ListReimagePending returns the set of machine ids (from the given slice) that
// are currently flagged for re-image. Batched so the machine list can badge
// pending re-images without an N+1 sweep. An empty input yields an empty set.
func (r *InventoryRepo) ListReimagePending(ctx context.Context, machineIDs []ID) (map[ID]bool, error) {
	out := map[ID]bool{}
	if len(machineIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(machineIDs))
	args := make([]any, len(machineIDs))
	for i, id := range machineIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM machine_record
		 WHERE reimage_pending=1 AND id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetByAgentID looks up a machine by AutoDeploy's server-minted agent_id
// (the object id the agent identifies itself with). Empty agentID never
// matches -- it's the transient default for un-backfilled rows.
func (r *InventoryRepo) GetByAgentID(ctx context.Context, agentID string) (MachineRecord, error) {
	if strings.TrimSpace(agentID) == "" {
		return MachineRecord{}, fmt.Errorf("agent_id: %w", ErrNotFound)
	}
	var v MachineRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT id, system_uuid, agent_id, system_serial, system_manufacturer, system_product,
		       bios_vendor, bios_version, board_manufacturer, board_product, board_serial,
		       first_seen, last_seen
		FROM machine_record WHERE agent_id=?`, agentID).Scan(
		&v.ID, &v.SystemUUID, &v.AgentID, &v.SystemSerial, &v.SystemManufacturer, &v.SystemProduct,
		&v.BIOSVendor, &v.BIOSVersion, &v.BoardManufacturer, &v.BoardProduct, &v.BoardSerial,
		&v.FirstSeen, &v.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRecord{}, fmt.Errorf("machine agent_id %s: %w", agentID, ErrNotFound)
	}
	return v, err
}

// List returns all machine records ordered by last_seen descending.
func (r *InventoryRepo) List(ctx context.Context) ([]MachineRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, system_uuid, system_serial, system_manufacturer, system_product,
		       bios_vendor, bios_version, board_manufacturer, board_product, board_serial,
		       reported_name, ad_dn, first_seen, last_seen
		FROM machine_record ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRecord
	for rows.Next() {
		var v MachineRecord
		if err := rows.Scan(&v.ID, &v.SystemUUID, &v.SystemSerial, &v.SystemManufacturer,
			&v.SystemProduct, &v.BIOSVendor, &v.BIOSVersion,
			&v.BoardManufacturer, &v.BoardProduct, &v.BoardSerial,
			&v.ReportedName, &v.ADDistinguishedName,
			&v.FirstSeen, &v.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// NamesByUUID maps each machine's SMBIOS system UUID to its best display
// name -- the agent-reported computer name, falling back to the operator-
// set binding name. This is the same precedence the machine list uses
// (ReportedName, then MachineName). Used to resolve a log event's actor
// (which carries the system UUID) to a human-readable machine name.
// Records with an empty UUID or no resolvable name are omitted.
func (r *InventoryRepo) NamesByUUID(ctx context.Context) (map[string]string, error) {
	records, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]ID, 0, len(records))
	for _, m := range records {
		ids = append(ids, m.ID)
	}
	bindings, err := r.ListBindingsForMachines(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(records))
	for _, m := range records {
		if m.SystemUUID == "" {
			continue
		}
		name := m.ReportedName
		if name == "" {
			name = bindings[m.ID].MachineName
		}
		if name != "" {
			out[m.SystemUUID] = name
		}
	}
	return out, nil
}

// NamesByID maps each machine's ID to its best display name, using the same
// precedence as NamesByUUID (agent-reported computer name, then the operator-
// set binding name). Machines with no resolvable name are omitted. Used to
// label rows that reference a machine by ID, such as bulk-operation jobs.
func (r *InventoryRepo) NamesByID(ctx context.Context) (map[ID]string, error) {
	records, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]ID, 0, len(records))
	for _, m := range records {
		ids = append(ids, m.ID)
	}
	bindings, err := r.ListBindingsForMachines(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[ID]string, len(records))
	for _, m := range records {
		name := m.ReportedName
		if name == "" {
			name = bindings[m.ID].MachineName
		}
		if name != "" {
			out[m.ID] = name
		}
	}
	return out, nil
}

// ListBindingsForMachines returns bindings for the given machine IDs in a
// single query. Machines without a binding are absent from the map.
func (r *InventoryRepo) ListBindingsForMachines(ctx context.Context, machineIDs []ID) (map[ID]MachineBinding, error) {
	if len(machineIDs) == 0 {
		return map[ID]MachineBinding{}, nil
	}
	placeholders := make([]string, len(machineIDs))
	args := make([]any, len(machineIDs))
	for i, id := range machineIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT machine_id, image_id, machine_name, target_ou, group_memberships, updated_at
		 FROM machine_binding WHERE machine_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ID]MachineBinding, len(machineIDs))
	for rows.Next() {
		var b MachineBinding
		var imageID sql.NullInt64
		var groups string
		if err := rows.Scan(&b.MachineID, &imageID, &b.MachineName, &b.TargetOU, &groups, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.ImageID = idPtr(imageID)
		if err := json.Unmarshal([]byte(groups), &b.GroupMemberships); err != nil {
			return nil, fmt.Errorf("parse group_memberships: %w", err)
		}
		out[b.MachineID] = b
	}
	return out, rows.Err()
}

// ListHistoryForMachines returns deployment records for the given machine IDs
// in a single query, grouped by machine ID, newest first.
func (r *InventoryRepo) ListHistoryForMachines(ctx context.Context, machineIDs []ID) (map[ID][]DeploymentRecord, error) {
	if len(machineIDs) == 0 {
		return map[ID][]DeploymentRecord{}, nil
	}
	placeholders := make([]string, len(machineIDs))
	args := make([]any, len(machineIDs))
	for i, id := range machineIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes, phase, progress_done, progress_total
		 FROM deployment_history
		 WHERE machine_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY started_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ID][]DeploymentRecord, len(machineIDs))
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
		out[d.MachineID] = append(out[d.MachineID], d)
	}
	return out, rows.Err()
}

// GetBinding returns the binding for a machine, or ErrNotFound.
func (r *InventoryRepo) GetBinding(ctx context.Context, machineID ID) (MachineBinding, error) {
	var b MachineBinding
	var imageID sql.NullInt64
	var groups string
	err := r.db.QueryRowContext(ctx, `
		SELECT machine_id, image_id, machine_name, target_ou, group_memberships, updated_at
		FROM machine_binding WHERE machine_id=?`, machineID).Scan(
		&b.MachineID, &imageID, &b.MachineName, &b.TargetOU, &groups, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineBinding{}, fmt.Errorf("binding for machine %d: %w", machineID, ErrNotFound)
	}
	if err != nil {
		return MachineBinding{}, err
	}
	b.ImageID = idPtr(imageID)
	if err := json.Unmarshal([]byte(groups), &b.GroupMemberships); err != nil {
		return MachineBinding{}, fmt.Errorf("parse group_memberships: %w", err)
	}
	return b, nil
}

// UpsertBinding writes the binding, replacing any existing one.
func (r *InventoryRepo) UpsertBinding(ctx context.Context, b MachineBinding) error {
	if b.GroupMemberships == nil {
		b.GroupMemberships = []string{}
	}
	groupsJSON, err := json.Marshal(b.GroupMemberships)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO machine_binding
		    (machine_id, image_id, machine_name, target_ou, group_memberships, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(machine_id) DO UPDATE SET
		    image_id=excluded.image_id,
		    machine_name=excluded.machine_name,
		    target_ou=excluded.target_ou,
		    group_memberships=excluded.group_memberships,
		    updated_at=CURRENT_TIMESTAMP`,
		b.MachineID, nullID(b.ImageID), b.MachineName, b.TargetOU, string(groupsJSON))
	return err
}

// EnsureBindingImage records the image a machine was deployed from when
// its binding doesn't already name one. Manually enrolled machines have
// no operator-created binding, so without this the inventory never shows
// what image is on the box; an operator-chosen image is never
// overwritten.
func (r *InventoryRepo) EnsureBindingImage(ctx context.Context, machineID, imageID ID) error {
	if imageID == 0 {
		return nil
	}
	b, err := r.GetBinding(ctx, machineID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		b = MachineBinding{MachineID: machineID}
	}
	if b.ImageID != nil && *b.ImageID != 0 {
		return nil
	}
	b.ImageID = &imageID
	return r.UpsertBinding(ctx, b)
}

// RecordDeployment opens a new dated history row in state 'in_progress'.
// The agent calls CompleteDeployment when the deployment finishes.
func (r *InventoryRepo) RecordDeployment(ctx context.Context, machineID ID, imageID *ID) (ID, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO deployment_history (machine_id, image_id, outcome, phase)
		VALUES (?, ?, 'in_progress', 'staging')`,
		machineID, nullID(imageID))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return ID(id), nil
}

// CompleteDeployment marks a deployment row with outcome and notes.
func (r *InventoryRepo) CompleteDeployment(ctx context.Context, id ID, outcome, notes string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE deployment_history
		SET outcome=?, notes=?, phase='complete', completed_at=CURRENT_TIMESTAMP
		WHERE id=?`, outcome, notes, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deployment %d: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateLatestDeployment sets the outcome/notes/phase on the machine's most
// recent deployment row. Used by the Boot Client to update a staging row
// it opened (staged -> note, failed -> failed). No open row is a no-op.
func (r *InventoryRepo) UpdateLatestDeployment(ctx context.Context, machineID ID, outcome, notes, phase string) error {
	completed := "completed_at=CURRENT_TIMESTAMP"
	if outcome == "in_progress" {
		completed = "completed_at=NULL"
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE deployment_history SET outcome=?, notes=?, phase=?, `+completed+`
		WHERE id = (SELECT id FROM deployment_history WHERE machine_id=?
		            ORDER BY started_at DESC LIMIT 1)`,
		outcome, notes, phase, machineID)
	return err
}

// UpdateLatestInProgressNote sets the notes on the machine's most recent
// deployment row ONLY IF that row is still in_progress. It never sets
// completed_at and never changes the outcome. Used by the unattend's
// install-status milestone callbacks: if the post-install agent has already
// closed the row (ok/failed), the milestone arrives late and must be a no-op
// rather than resurrecting the row. The `AND outcome='in_progress'` guard on
// the outer UPDATE is that race protection.
func (r *InventoryRepo) UpdateLatestInProgressNote(ctx context.Context, machineID ID, notes, phase string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE deployment_history SET notes=?, phase=?
		WHERE id = (SELECT id FROM deployment_history WHERE machine_id=?
		            ORDER BY started_at DESC LIMIT 1)
		  AND outcome='in_progress'`,
		notes, phase, machineID)
	return err
}

// SetDeploymentProgress updates the phase, notes and software-install counters
// on a specific deployment row, addressed by its id (the agent learns it from
// /api/v1/agent/self). Like UpdateLatestInProgressNote it is race-safe: the
// `AND outcome='in_progress'` guard makes a late progress post a no-op once the
// agent's final ok/failed report has already closed the row. Used by the
// agent's deploy-progress callback to drive the live bar through the software
// phase (agent-online, then installing done/total).
func (r *InventoryRepo) SetDeploymentProgress(ctx context.Context, id ID, phase, notes string, done, total int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE deployment_history SET phase=?, notes=?, progress_done=?, progress_total=?
		WHERE id=? AND outcome='in_progress'`,
		phase, notes, done, total, id)
	return err
}

// Delete removes a machine and all rows that reference it (binding,
// deployment history, detected state). Done in one transaction so
// inventory can never be left with dangling references.
func (r *InventoryRepo) Delete(ctx context.Context, id ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM machine_binding WHERE machine_id=?`,
		`DELETE FROM deployment_history WHERE machine_id=?`,
		`DELETE FROM machine_detected_state WHERE machine_id=?`,
		`DELETE FROM bulk_job WHERE machine_id=?`,
		`DELETE FROM reimage_event WHERE machine_id=?`,
		`DELETE FROM machine_record WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LatestOpenDeployment returns the most recent in_progress deployment for the
// machine, or ErrNotFound if there is none. Used by /api/v1/agent/self to tell
// the resident agent about a deployment the boot client opened so the agent can
// close it once software installation finishes.
func (r *InventoryRepo) LatestOpenDeployment(ctx context.Context, machineID ID) (DeploymentRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes, phase, progress_done, progress_total
		FROM deployment_history
		WHERE machine_id=? AND outcome='in_progress'
		ORDER BY started_at DESC
		LIMIT 1`, machineID)
	var d DeploymentRecord
	var imageID sql.NullInt64
	var completed sql.NullTime
	if err := row.Scan(&d.ID, &d.MachineID, &imageID, &d.StartedAt,
		&completed, &d.Outcome, &d.Notes, &d.Phase, &d.ProgressDone, &d.ProgressTotal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeploymentRecord{}, ErrNotFound
		}
		return DeploymentRecord{}, err
	}
	d.ImageID = idPtr(imageID)
	if completed.Valid {
		t := completed.Time
		d.CompletedAt = &t
	}
	return d, nil
}

// HistoryFor returns dated deployment rows for the machine, newest first.
func (r *InventoryRepo) HistoryFor(ctx context.Context, machineID ID) ([]DeploymentRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes, phase, progress_done, progress_total
		FROM deployment_history
		WHERE machine_id=?
		ORDER BY started_at DESC`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentRecord
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
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordDetectedState replaces the agent's reported detection state for the
// (machine, package) pair.
func (r *InventoryRepo) RecordDetectedState(ctx context.Context, s DetectedState) error {
	detected := 0
	if s.Detected {
		detected = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO machine_detected_state
		    (machine_id, software_package_id, detected, last_evaluated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(machine_id, software_package_id) DO UPDATE SET
		    detected=excluded.detected,
		    last_evaluated_at=CURRENT_TIMESTAMP`,
		s.MachineID, s.SoftwarePackageID, detected)
	return err
}

// OSCount is one row of the OS distribution breakdown.
type OSCount struct {
	OS    string
	Count int
}

// OSBreakdown groups machines by reported OS caption and returns counts
// sorted by count descending. Machines without hardware data are skipped.
func (r *InventoryRepo) OSBreakdown(ctx context.Context) ([]OSCount, error) {
	machines, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	// List doesn't load hardware_json; fetch each machine for its Hardware.
	counts := map[string]int{}
	for _, m := range machines {
		full, err := r.Get(ctx, m.ID)
		if err != nil || full.Hardware == nil || full.Hardware.OSCaption == "" {
			continue
		}
		counts[full.Hardware.OSCaption]++
	}
	out := make([]OSCount, 0, len(counts))
	for os, n := range counts {
		out = append(out, OSCount{OS: os, Count: n})
	}
	// Sort by count desc, then name asc.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count || (out[j].Count == out[i].Count && out[j].OS < out[i].OS) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// DetectedStateFor returns the latest detected state per package for a
// machine. Use for drift reporting in the portal.
func (r *InventoryRepo) DetectedStateFor(ctx context.Context, machineID ID) ([]DetectedState, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT machine_id, software_package_id, detected, last_evaluated_at
		FROM machine_detected_state WHERE machine_id=?`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DetectedState
	for rows.Next() {
		var s DetectedState
		var d int
		if err := rows.Scan(&s.MachineID, &s.SoftwarePackageID, &d, &s.LastEvaluatedAt); err != nil {
			return nil, err
		}
		s.Detected = d != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

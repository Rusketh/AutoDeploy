package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
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
	AgentID            string    `json:"agent_id"`
	SystemSerial       string    `json:"system_serial"`
	SystemManufacturer string    `json:"system_manufacturer"`
	SystemProduct      string    `json:"system_product"`
	BIOSVendor         string    `json:"bios_vendor"`
	BIOSVersion        string    `json:"bios_version"`
	BoardManufacturer  string    `json:"board_manufacturer"`
	BoardProduct       string    `json:"board_product"`
	BoardSerial        string    `json:"board_serial"`
	FirstSeen          time.Time `json:"first_seen"`
	LastSeen           time.Time `json:"last_seen"`
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
		// Update identity fields (a firmware update can change vendor
		// strings) and last_seen. Backfill agent_id if this is a row that
		// predates migration 0012 (still empty); never overwrite an id
		// already minted.
		_, err := r.db.ExecContext(ctx, `
			UPDATE machine_record
			SET system_serial=?, system_manufacturer=?, system_product=?,
			    bios_vendor=?, bios_version=?, board_manufacturer=?, board_product=?, board_serial=?,
			    agent_id=CASE WHEN agent_id='' THEN ? ELSE agent_id END,
			    last_seen=CURRENT_TIMESTAMP
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
	return r.Get(ctx, ID(newID))
}

// Get returns the machine record by primary key.
func (r *InventoryRepo) Get(ctx context.Context, id ID) (MachineRecord, error) {
	var v MachineRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT id, system_uuid, agent_id, system_serial, system_manufacturer, system_product,
		       bios_vendor, bios_version, board_manufacturer, board_product, board_serial,
		       first_seen, last_seen
		FROM machine_record WHERE id=?`, id).Scan(
		&v.ID, &v.SystemUUID, &v.AgentID, &v.SystemSerial, &v.SystemManufacturer, &v.SystemProduct,
		&v.BIOSVendor, &v.BIOSVersion, &v.BoardManufacturer, &v.BoardProduct, &v.BoardSerial,
		&v.FirstSeen, &v.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRecord{}, fmt.Errorf("machine %d: %w", id, ErrNotFound)
	}
	return v, err
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
		       first_seen, last_seen
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
			&v.FirstSeen, &v.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, v)
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

// RecordDeployment opens a new dated history row in state 'in_progress'.
// The agent calls CompleteDeployment when the deployment finishes.
func (r *InventoryRepo) RecordDeployment(ctx context.Context, machineID ID, imageID *ID) (ID, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO deployment_history (machine_id, image_id, outcome)
		VALUES (?, ?, 'in_progress')`,
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
		SET outcome=?, notes=?, completed_at=CURRENT_TIMESTAMP
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

// HistoryFor returns dated deployment rows for the machine, newest first.
func (r *InventoryRepo) HistoryFor(ctx context.Context, machineID ID) ([]DeploymentRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes
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
			&completed, &d.Outcome, &d.Notes); err != nil {
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

// IssueDeployToken rotates the per-machine deploy token. Returns the
// cleartext token (caller's responsibility to hand it to the agent
// and never log it). Only the SHA-256 hash is persisted. ttl is how
// long the token stays valid; rotate-on-every-deploy means we never
// need a long-lived token.
func (r *InventoryRepo) IssueDeployToken(ctx context.Context, machineID ID, ttl time.Duration) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(sum[:])
	expires := time.Now().Add(ttl).UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO machine_deploy_token (machine_id, token_hash, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(machine_id) DO UPDATE SET
		    token_hash=excluded.token_hash,
		    issued_at=CURRENT_TIMESTAMP,
		    expires_at=excluded.expires_at`,
		machineID, hashHex, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

// ValidateDeployToken returns true if token matches the stored hash
// for machineID and hasn't expired. Constant-time compare prevents a
// timing oracle. An empty token is always invalid.
func (r *InventoryRepo) ValidateDeployToken(ctx context.Context, machineID ID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var hashHex string
	var expires time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT token_hash, expires_at FROM machine_deploy_token WHERE machine_id=?`,
		machineID).Scan(&hashHex, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().After(expires) {
		return false, nil
	}
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(hashHex), []byte(want)) != 1 {
		return false, nil
	}
	return true, nil
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

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// BitLockerPIN is the per-machine pre-boot PIN setting. PIN is never
// returned in this struct; only PINSet flag indicates whether one is
// configured. The actual value is retrieved via RetrievePIN which is a
// privileged, audited operation.
type BitLockerPIN struct {
	MachineID ID        `json:"machine_id"`
	PINSet    bool      `json:"pin_set"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecoveryKeyRecord is one escrowed recovery key. The Key field is the
// cleartext key — only populated by RetrieveRecoveryKey (audited).
type RecoveryKeyRecord struct {
	ID         ID        `json:"id"`
	MachineID  ID        `json:"machine_id"`
	Note       string    `json:"note,omitempty"`
	Key        string    `json:"key,omitempty"`
	EscrowedAt time.Time `json:"escrowed_at"`
}

// BitLockerRepo owns bitlocker_pin and bitlocker_recovery_key.
type BitLockerRepo struct {
	db *storage.DB
	bx *secrets.Box
}

// NewBitLockerRepo binds the repo to a storage handle and a secrets box.
func NewBitLockerRepo(db *storage.DB, bx *secrets.Box) *BitLockerRepo {
	return &BitLockerRepo{db: db, bx: bx}
}

// SetPIN stores (or clears) the PIN. An empty pin removes the row
// entirely, which the design treats as "do not encrypt".
func (r *BitLockerRepo) SetPIN(ctx context.Context, machineID ID, pin string) error {
	if pin == "" {
		_, err := r.db.ExecContext(ctx,
			`DELETE FROM bitlocker_pin WHERE machine_id=?`, machineID)
		return err
	}
	ct, err := r.bx.Encrypt(pin)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO bitlocker_pin (machine_id, pin_cipher) VALUES (?, ?)
		ON CONFLICT(machine_id) DO UPDATE SET
		    pin_cipher=excluded.pin_cipher,
		    updated_at=CURRENT_TIMESTAMP`,
		machineID, ct)
	return err
}

// ListPINStatuses returns PINSet status for a batch of machine IDs in a
// single query. Machines without a PIN row are absent from the map.
func (r *BitLockerRepo) ListPINStatuses(ctx context.Context, machineIDs []ID) (map[ID]BitLockerPIN, error) {
	if len(machineIDs) == 0 {
		return map[ID]BitLockerPIN{}, nil
	}
	placeholders := make([]string, len(machineIDs))
	args := make([]any, len(machineIDs))
	for i, id := range machineIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT machine_id, pin_cipher, updated_at FROM bitlocker_pin
		 WHERE machine_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ID]BitLockerPIN, len(machineIDs))
	for rows.Next() {
		var v BitLockerPIN
		var ct string
		if err := rows.Scan(&v.MachineID, &ct, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.PINSet = ct != ""
		out[v.MachineID] = v
	}
	return out, rows.Err()
}

// PINStatus reports whether a PIN is configured. Does NOT decrypt.
func (r *BitLockerRepo) PINStatus(ctx context.Context, machineID ID) (BitLockerPIN, error) {
	var v BitLockerPIN
	var ct string
	err := r.db.QueryRowContext(ctx,
		`SELECT machine_id, pin_cipher, updated_at FROM bitlocker_pin WHERE machine_id=?`,
		machineID).Scan(&v.MachineID, &ct, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BitLockerPIN{MachineID: machineID, PINSet: false}, nil
	}
	if err != nil {
		return BitLockerPIN{}, err
	}
	v.PINSet = ct != ""
	return v, nil
}

// RetrievePIN returns the cleartext PIN for use by the agent or operator.
// Calls to this function are the audited secret-access boundary.
func (r *BitLockerRepo) RetrievePIN(ctx context.Context, machineID ID) (string, error) {
	var ct string
	err := r.db.QueryRowContext(ctx,
		`SELECT pin_cipher FROM bitlocker_pin WHERE machine_id=?`, machineID).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no PIN for machine %d: %w", machineID, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	return r.bx.Decrypt(ct)
}

// EscrowRecoveryKey appends a recovery key. Never overwrites; full
// history is retained.
func (r *BitLockerRepo) EscrowRecoveryKey(ctx context.Context, machineID ID, key, note string) error {
	if key == "" {
		return fmt.Errorf("%w: recovery key empty", ErrValidation)
	}
	ct, err := r.bx.Encrypt(key)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO bitlocker_recovery_key (machine_id, key_cipher, note) VALUES (?, ?, ?)`,
		machineID, ct, note)
	return err
}

// ListRecoveryKeys returns the escrow history for a machine, newest
// first. Key values are NOT decrypted; the Key field is empty.
func (r *BitLockerRepo) ListRecoveryKeys(ctx context.Context, machineID ID) ([]RecoveryKeyRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, note, escrowed_at
		FROM bitlocker_recovery_key
		WHERE machine_id=?
		ORDER BY escrowed_at DESC`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecoveryKeyRecord
	for rows.Next() {
		var v RecoveryKeyRecord
		if err := rows.Scan(&v.ID, &v.MachineID, &v.Note, &v.EscrowedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RetrieveRecoveryKey returns the cleartext recovery key with the given
// id. Audited secret-access boundary.
func (r *BitLockerRepo) RetrieveRecoveryKey(ctx context.Context, id ID) (RecoveryKeyRecord, error) {
	var v RecoveryKeyRecord
	var ct string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, machine_id, note, escrowed_at, key_cipher
		 FROM bitlocker_recovery_key WHERE id=?`, id).Scan(
		&v.ID, &v.MachineID, &v.Note, &v.EscrowedAt, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryKeyRecord{}, fmt.Errorf("recovery key %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return RecoveryKeyRecord{}, err
	}
	pt, err := r.bx.Decrypt(ct)
	if err != nil {
		return RecoveryKeyRecord{}, err
	}
	v.Key = pt
	return v, nil
}

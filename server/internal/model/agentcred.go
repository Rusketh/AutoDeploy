package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Agent credentials: each machine's agent proves who it is with its public
// agent_id plus a random secret. Only SHA-256 hashes of secrets are stored
// (a secret is 32 random bytes, so a plain hash is not brute-forceable).
//
// Rotation keeps the agent and server in step whatever fails part-way. The
// agent generates the next secret and persists it as "pending" BEFORE asking;
// the server makes it current and keeps the old one valid as "previous" until
// the agent first authenticates with the new one. So at every moment the
// server accepts at least one secret the agent holds, and a retried rotation
// is idempotent.

// Credential states as shown to operators.
const (
	// CredLegacy: never issued a secret (an agent from before secrets, or a
	// machine not yet deployed). Talks the old way while legacy auth is on.
	CredLegacy = "legacy"
	// CredSecured: holds a secret; every agent request must present it.
	CredSecured = "secured"
	// CredApproval: no usable secret (revoked, or a request was refused):
	// the next request for one needs an operator's approval.
	CredApproval = "approval_required"
)

// Credential request states.
const (
	CredRequestPending  = "pending"
	CredRequestApproved = "approved"
	CredRequestDenied   = "denied"
	CredRequestRedeemed = "redeemed"
)

// ErrBadSecret is returned when a presented agent secret doesn't match.
var ErrBadSecret = errors.New("agent secret does not match")

// AgentCredential is a machine's credential state (never the secret).
type AgentCredential struct {
	MachineID ID
	Status    string
	IssuedVia string
	IssuedAt  *time.Time
	RotatedAt *time.Time
	LastUsed  *time.Time
	// RotationUnconfirmed is true between a rotation and the agent's first
	// request with the new secret (the previous secret still works).
	RotationUnconfirmed bool
}

// CredentialRequest is a request for a secret awaiting (or past) approval.
type CredentialRequest struct {
	ID         ID
	MachineID  ID
	Status     string
	Reason     string
	RemoteAddr string
	DecidedBy  string
	DecidedAt  *time.Time
	CreatedAt  time.Time
}

// AgentCredentialRepo stores agent credentials and their supporting grants.
type AgentCredentialRepo struct{ db *storage.DB }

// NewAgentCredentialRepo binds the repo to a database.
func NewAgentCredentialRepo(db *storage.DB) *AgentCredentialRepo {
	return &AgentCredentialRepo{db: db}
}

// NewAgentSecret returns a fresh random secret (32 bytes, base64url).
func NewAgentSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// MinSecretLength is the shortest secret accepted (an agent-generated one
// must carry at least 32 bytes of entropy; 43 base64url characters).
const MinSecretLength = 43

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashEq(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type credRow struct {
	cur, prev, via  string
	issued, rotated sql.NullTime
	used            sql.NullTime
	exists          bool
}

func (r *AgentCredentialRepo) row(ctx context.Context, machineID ID) (credRow, error) {
	var c credRow
	err := r.db.QueryRowContext(ctx, `
		SELECT secret_hash, prev_secret_hash, issued_via, issued_at, rotated_at, last_used_at
		FROM agent_credential WHERE machine_id=?`, int64(machineID)).Scan(
		&c.cur, &c.prev, &c.via, &c.issued, &c.rotated, &c.used)
	if errors.Is(err, sql.ErrNoRows) {
		return credRow{}, nil
	}
	if err != nil {
		return credRow{}, err
	}
	c.exists = true
	return c, nil
}

func nt(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// Get returns a machine's credential state.
func (r *AgentCredentialRepo) Get(ctx context.Context, machineID ID) (AgentCredential, error) {
	c, err := r.row(ctx, machineID)
	if err != nil {
		return AgentCredential{}, err
	}
	out := AgentCredential{MachineID: machineID, Status: CredLegacy}
	if !c.exists {
		return out, nil
	}
	out.IssuedVia = c.via
	out.IssuedAt, out.RotatedAt, out.LastUsed = nt(c.issued), nt(c.rotated), nt(c.used)
	if c.cur == "" {
		out.Status = CredApproval
		return out, nil
	}
	out.Status = CredSecured
	out.RotationUnconfirmed = c.prev != ""
	return out, nil
}

// Issue mints a new secret for the machine, replacing any existing one (and
// any rotation in progress), and consumes its deploy grant. via records how
// it was authorised ("pxe", "first-contact", "approved", "media", ...).
// The returned secret is shown once and never stored.
//
// With keepPrevious, the machine's current secret stays valid (as the
// "previous" one) until the new secret is first used. A network deploy uses
// this: if imaging fails and the machine boots back into its existing
// Windows, that agent keeps working; once the new OS's agent authenticates,
// the old secret stops working.
func (r *AgentCredentialRepo) Issue(ctx context.Context, machineID ID, via string, keepPrevious bool) (string, error) {
	secret, err := NewAgentSecret()
	if err != nil {
		return "", err
	}
	prev := ""
	if keepPrevious {
		if c, err := r.row(ctx, machineID); err == nil && c.exists {
			prev = c.cur
		}
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO agent_credential (machine_id, secret_hash, prev_secret_hash, issued_via, issued_at, rotated_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id) DO UPDATE SET
		    secret_hash=excluded.secret_hash, prev_secret_hash=excluded.prev_secret_hash,
		    issued_via=excluded.issued_via, issued_at=excluded.issued_at,
		    rotated_at=excluded.rotated_at, last_used_at=NULL, updated_at=excluded.updated_at`,
		int64(machineID), hashSecret(secret), prev, via, now, now, now)
	if err != nil {
		return "", err
	}
	_, _ = r.db.ExecContext(ctx, `DELETE FROM agent_deploy_grant WHERE machine_id=?`, int64(machineID))
	return secret, nil
}

// Verify checks a presented secret. ok is true for the current secret or,
// while a rotation is unconfirmed, the previous one. Using the current
// secret confirms a pending rotation (the previous one stops working).
func (r *AgentCredentialRepo) Verify(ctx context.Context, machineID ID, secret string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	c, err := r.row(ctx, machineID)
	if err != nil || !c.exists || c.cur == "" {
		return false, err
	}
	h := hashSecret(secret)
	switch {
	case hashEq(h, c.cur):
		now := time.Now().UTC()
		// Confirm a rotation; refresh last_used at most once a minute.
		if c.prev != "" || !c.used.Valid || now.Sub(c.used.Time) > time.Minute {
			_, _ = r.db.ExecContext(ctx, `
				UPDATE agent_credential SET prev_secret_hash='', last_used_at=?
				WHERE machine_id=? AND secret_hash=?`, now, int64(machineID), c.cur)
		}
		return true, nil
	case hashEq(h, c.prev):
		return true, nil
	}
	return false, nil
}

// Rotate replaces the machine's secret with newSecret, authorised by the
// presented (current, or still-valid previous) secret. Idempotent: repeating
// a rotation the server already applied succeeds without change.
func (r *AgentCredentialRepo) Rotate(ctx context.Context, machineID ID, presented, newSecret string) error {
	c, err := r.row(ctx, machineID)
	if err != nil {
		return err
	}
	if !c.exists || c.cur == "" || presented == "" {
		return ErrBadSecret
	}
	hp, hn := hashSecret(presented), hashSecret(newSecret)
	if !hashEq(hp, c.cur) && !hashEq(hp, c.prev) {
		return ErrBadSecret
	}
	if len(newSecret) < MinSecretLength {
		return fmt.Errorf("%w: new secret too short", ErrValidation)
	}
	if hashEq(hn, c.cur) {
		return nil // already applied (a retried rotation)
	}
	if hashEq(hn, hp) {
		return fmt.Errorf("%w: new secret must differ", ErrValidation)
	}
	// The secret the agent still holds stays valid until it uses the new one.
	_, err = r.db.ExecContext(ctx, `
		UPDATE agent_credential SET secret_hash=?, prev_secret_hash=?, rotated_at=?, updated_at=?
		WHERE machine_id=?`, hn, hp, time.Now().UTC(), time.Now().UTC(), int64(machineID))
	return err
}

// Revoke removes a machine's secret. Its agent stops working until an
// operator approves a new request (first-contact issuing no longer applies).
func (r *AgentCredentialRepo) Revoke(ctx context.Context, machineID ID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_credential (machine_id, secret_hash, prev_secret_hash, issued_via, updated_at)
		VALUES (?, '', '', 'revoked', ?)
		ON CONFLICT(machine_id) DO UPDATE SET
		    secret_hash='', prev_secret_hash='', issued_via='revoked', updated_at=excluded.updated_at`,
		int64(machineID), time.Now().UTC())
	if err == nil {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM agent_deploy_grant WHERE machine_id=?`, int64(machineID))
	}
	return err
}

// LegacyMachineCount counts enrolled machines (they have an agent_id) that
// have never been issued a secret.
func (r *AgentCredentialRepo) LegacyMachineCount(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM machine_record m
		WHERE m.agent_id != '' AND NOT EXISTS (SELECT 1 FROM agent_credential c WHERE c.machine_id=m.id)`).Scan(&n)
	return n, err
}

// ---- Requests needing approval -------------------------------------------

// RequestCredential records (or finds) a request identified by nonce and
// returns it; created is true when this call created it (a new request is
// pending).
func (r *AgentCredentialRepo) RequestCredential(ctx context.Context, machineID ID, nonce, reason, remoteAddr string) (req CredentialRequest, created bool, err error) {
	nh := hashSecret(nonce)
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_credential_request (machine_id, nonce_hash, reason, remote_addr)
		VALUES (?, ?, ?, ?) ON CONFLICT(machine_id, nonce_hash) DO NOTHING`,
		int64(machineID), nh, reason, remoteAddr)
	if err != nil {
		return CredentialRequest{}, false, err
	}
	n, _ := res.RowsAffected()
	req, err = r.requestBy(ctx, `machine_id=? AND nonce_hash=?`, int64(machineID), nh)
	return req, n > 0, err
}

func (r *AgentCredentialRepo) requestBy(ctx context.Context, where string, args ...any) (CredentialRequest, error) {
	var v CredentialRequest
	var decided sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, machine_id, status, reason, remote_addr, decided_by, decided_at, created_at
		FROM agent_credential_request WHERE `+where, args...).Scan(
		&v.ID, &v.MachineID, &v.Status, &v.Reason, &v.RemoteAddr, &v.DecidedBy, &decided, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("credential request: %w", ErrNotFound)
	}
	v.DecidedAt = nt(decided)
	return v, err
}

// MarkRedeemed records that an approved request's secret was handed out.
func (r *AgentCredentialRepo) MarkRedeemed(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agent_credential_request SET status='redeemed' WHERE id=? AND status='approved'`, int64(id))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("credential request %d is not approved: %w", id, ErrConflict)
	}
	return nil
}

// Decide approves or denies a pending request. Approving one denies the
// machine's other pending requests (only one requester gets the secret).
func (r *AgentCredentialRepo) Decide(ctx context.Context, id ID, approve bool, by string) (CredentialRequest, error) {
	req, err := r.requestBy(ctx, `id=?`, int64(id))
	if err != nil {
		return req, err
	}
	if req.Status != CredRequestPending {
		return req, fmt.Errorf("%w: request is already %s", ErrConflict, req.Status)
	}
	status := CredRequestDenied
	if approve {
		status = CredRequestApproved
	}
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE agent_credential_request SET status=?, decided_by=?, decided_at=? WHERE id=?`,
		status, by, now, int64(id)); err != nil {
		return req, err
	}
	if approve {
		_, _ = r.db.ExecContext(ctx, `
			UPDATE agent_credential_request SET status='denied', decided_by=?, decided_at=?
			WHERE machine_id=? AND id!=? AND status='pending'`, by, now, int64(req.MachineID), int64(id))
	}
	return r.requestBy(ctx, `id=?`, int64(id))
}

// Requests lists a machine's recent requests, newest first.
func (r *AgentCredentialRepo) Requests(ctx context.Context, machineID ID, limit int) ([]CredentialRequest, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, machine_id, status, reason, remote_addr, decided_by, decided_at, created_at
		FROM agent_credential_request WHERE machine_id=? ORDER BY id DESC LIMIT ?`, int64(machineID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialRequest
	for rows.Next() {
		var v CredentialRequest
		var decided sql.NullTime
		if err := rows.Scan(&v.ID, &v.MachineID, &v.Status, &v.Reason, &v.RemoteAddr, &v.DecidedBy, &decided, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.DecidedAt = nt(decided)
		out = append(out, v)
	}
	return out, rows.Err()
}

// PendingRequestCount counts requests awaiting an operator.
func (r *AgentCredentialRepo) PendingRequestCount(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_credential_request WHERE status='pending'`).Scan(&n)
	return n, err
}

// ---- Deploy authorisation ----------------------------------------------------

// AuthorizeDeploy records that a boot client for systemUUID passed the access
// PIN just now.
func (r *AgentCredentialRepo) AuthorizeDeploy(ctx context.Context, systemUUID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO deploy_authorization (system_uuid, authorized_at) VALUES (?, ?)
		ON CONFLICT(system_uuid) DO UPDATE SET authorized_at=excluded.authorized_at`,
		strings.ToUpper(strings.TrimSpace(systemUUID)), time.Now().UTC())
	return err
}

// DeployAuthorized reports whether systemUUID passed the access PIN within
// the last window.
func (r *AgentCredentialRepo) DeployAuthorized(ctx context.Context, systemUUID string, window time.Duration) bool {
	var at time.Time
	err := r.db.QueryRowContext(ctx, `SELECT authorized_at FROM deploy_authorization WHERE system_uuid=?`,
		strings.ToUpper(strings.TrimSpace(systemUUID))).Scan(&at)
	return err == nil && time.Since(at) <= window
}

// GrantDeploy gives the machine's freshly installed OS a one-time right to
// obtain a new secret (for boot clients that can't carry one themselves).
func (r *AgentCredentialRepo) GrantDeploy(ctx context.Context, machineID ID, ttl time.Duration) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_deploy_grant (machine_id, expires_at) VALUES (?, ?)
		ON CONFLICT(machine_id) DO UPDATE SET expires_at=excluded.expires_at`,
		int64(machineID), time.Now().UTC().Add(ttl))
	return err
}

// RedeemDeployGrant consumes a valid deploy grant; true if there was one.
func (r *AgentCredentialRepo) RedeemDeployGrant(ctx context.Context, machineID ID) bool {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM agent_deploy_grant WHERE machine_id=? AND expires_at > ?`, int64(machineID), time.Now().UTC())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ---- Exported media ----------------------------------------------------------

// NewImageEnrollToken creates (replacing any earlier one) the token an
// exported USB/ISO of the image carries. Re-exporting invalidates the token
// of older media.
func (r *AgentCredentialRepo) NewImageEnrollToken(ctx context.Context, imageID ID, ttl time.Duration) (string, error) {
	tok, err := NewAgentSecret()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO image_enroll_token (image_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(image_id) DO UPDATE SET token_hash=excluded.token_hash,
		    created_at=excluded.created_at, expires_at=excluded.expires_at`,
		int64(imageID), hashSecret(tok), now, now.Add(ttl))
	if err != nil {
		return "", err
	}
	return tok, nil
}

// ValidImageEnrollToken reports whether token is the image's current,
// unexpired media token.
func (r *AgentCredentialRepo) ValidImageEnrollToken(ctx context.Context, imageID ID, token string) bool {
	if token == "" {
		return false
	}
	var h string
	var exp time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT token_hash, expires_at FROM image_enroll_token WHERE image_id=?`, int64(imageID)).Scan(&h, &exp)
	return err == nil && time.Now().Before(exp) && hashEq(hashSecret(token), h)
}

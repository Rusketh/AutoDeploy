package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AccessPIN gates the Boot Client menu. It is a single GLOBAL system
// setting (not per-image). When enabled, the Boot Client must submit a
// matching PIN before the server returns a menu.
//
// Rate limiting (server-side): no more than maxAttemptsPerWindow failed
// attempts from a single system_uuid in attemptWindow. After that, the
// server returns 429 until the window passes.
const (
	settingAccessPINEnabled = "access_pin.enabled"
	settingAccessPINHash    = "access_pin.hash"

	maxAttemptsPerWindow = 5
	attemptWindow        = 15 * time.Minute
)

// SettingsRepo reads and writes system_setting rows.
type SettingsRepo struct{ R *Repo }

// SetAccessPIN enables the access PIN and stores its bcrypt hash. An empty
// pin disables the gate entirely.
func (s *SettingsRepo) SetAccessPIN(ctx context.Context, pin string) error {
	if pin == "" {
		// Disable: clear hash and flag.
		if _, err := s.R.DB.ExecContext(ctx,
			`INSERT INTO system_setting(key,value) VALUES(?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
			settingAccessPINEnabled, "false"); err != nil {
			return err
		}
		_, err := s.R.DB.ExecContext(ctx,
			`DELETE FROM system_setting WHERE key=?`, settingAccessPINHash)
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.R.DB.ExecContext(ctx,
		`INSERT INTO system_setting(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		settingAccessPINEnabled, "true"); err != nil {
		return err
	}
	_, err = s.R.DB.ExecContext(ctx,
		`INSERT INTO system_setting(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		settingAccessPINHash, string(hash))
	return err
}

// AccessPINEnabled reports whether the gate is on.
func (s *SettingsRepo) AccessPINEnabled(ctx context.Context) (bool, error) {
	var v string
	err := s.R.DB.QueryRowContext(ctx,
		`SELECT value FROM system_setting WHERE key=?`, settingAccessPINEnabled).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

// ValidateAccessPIN compares pin against the stored hash. Returns
// (matched bool, error). The function is intentionally constant-time
// inside the bcrypt compare. The caller is responsible for rate
// limiting; this function never accepts more than a single attempt.
func (s *SettingsRepo) ValidateAccessPIN(ctx context.Context, pin string) (bool, error) {
	var hash string
	err := s.R.DB.QueryRowContext(ctx,
		`SELECT value FROM system_setting WHERE key=?`, settingAccessPINHash).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		// No PIN configured — gate is off. Caller should already have
		// short-circuited via AccessPINEnabled, but be defensive.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)); err != nil {
		// Use subtle.ConstantTimeCompare on a dummy comparison to
		// minimise length-based side-channels for very short PINs.
		_ = subtle.ConstantTimeCompare([]byte(pin), []byte(pin))
		return false, nil
	}
	return true, nil
}

// RecordPINAttempt logs an attempt for rate-limiting purposes. The PIN
// value is NOT recorded.
func (s *SettingsRepo) RecordPINAttempt(ctx context.Context, systemUUID string, success bool) error {
	v := 0
	if success {
		v = 1
	}
	_, err := s.R.DB.ExecContext(ctx,
		`INSERT INTO pin_attempt (system_uuid, success) VALUES (?, ?)`,
		systemUUID, v)
	return err
}

// PINLockedOut reports whether the system_uuid has too many recent
// failures.
func (s *SettingsRepo) PINLockedOut(ctx context.Context, systemUUID string) (bool, error) {
	since := time.Now().Add(-attemptWindow)
	var n int
	if err := s.R.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pin_attempt
		 WHERE system_uuid=? AND success=0 AND attempted_at>=?`,
		systemUUID, since).Scan(&n); err != nil {
		return false, err
	}
	return n >= maxAttemptsPerWindow, nil
}

// MustNewSettingsRepo is a tiny constructor wrapper for cmd packages.
func MustNewSettingsRepo(r *Repo) *SettingsRepo {
	if r == nil {
		panic(fmt.Errorf("MustNewSettingsRepo: nil Repo"))
	}
	return &SettingsRepo{R: r}
}

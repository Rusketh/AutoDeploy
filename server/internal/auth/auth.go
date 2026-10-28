// Package auth owns portal authentication: local user accounts with
// bcrypt-hashed passwords and cookie-backed sessions.
//
// Secret-handling rules (CONVENTIONS.md §4):
//   - Passwords NEVER appear in logs or HTTP response bodies. Only their
//     hashes are stored, and only the fact (not value) of password
//     operations is logged.
//   - Session tokens are 32-byte random values, base64-URL-encoded; they
//     are bearer credentials in the same risk class as passwords and
//     are not logged either.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// User is a local portal account. PasswordHash is the only field
// containing the password material; the cleartext is never stored.
type User struct {
	ID        int64
	Username  string
	Email     string
	Disabled  bool
	CreatedAt time.Time
}

// Repo owns user_account and user_session.
type Repo struct{ DB *storage.DB }

// New returns a Repo bound to db.
func New(db *storage.DB) *Repo { return &Repo{DB: db} }

// CreateUser inserts a new account with a bcrypt-hashed password. Returns
// the created user.
func (r *Repo) CreateUser(ctx context.Context, username, password string) (User, error) {
	if username == "" || password == "" {
		return User{}, errors.New("username and password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("hash: %w", err)
	}
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO user_account (username, password_hash) VALUES (?, ?)`,
		username, string(hash))
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return r.GetUser(ctx, id)
}

// GetUser returns the account by id.
func (r *Repo) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	var disabled int
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, username, email, disabled, created_at FROM user_account WHERE id=?`,
		id).Scan(&u.ID, &u.Username, &u.Email, &disabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, errors.New("user not found")
	}
	if err != nil {
		return User{}, err
	}
	u.Disabled = disabled != 0
	return u, nil
}

// ListUsers returns all accounts.
func (r *Repo) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, username, email, disabled, created_at FROM user_account ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &disabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword replaces the password hash for an existing account.
func (r *Repo) SetPassword(ctx context.Context, id int64, password string) error {
	if password == "" {
		return errors.New("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx,
		`UPDATE user_account SET password_hash=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		string(hash), id)
	return err
}

// SetEmail updates the email address for an account.
func (r *Repo) SetEmail(ctx context.Context, id int64, email string) error {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE user_account SET email=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		email, id)
	return err
}

// SetDisabled toggles the disabled flag.
func (r *Repo) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	d := 0
	if disabled {
		d = 1
	}
	_, err := r.DB.ExecContext(ctx,
		`UPDATE user_account SET disabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		d, id)
	return err
}

// Delete removes the account.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM user_account WHERE id=?`, id)
	return err
}

// Authenticate verifies username + password. On success returns the user;
// otherwise an error (without distinguishing user-not-found from
// wrong-password, to limit information leak).
func (r *Repo) Authenticate(ctx context.Context, username, password string) (User, error) {
	var u User
	var disabled int
	var hash string
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, username, email, disabled, password_hash, created_at
		 FROM user_account WHERE username=?`, username).Scan(
		&u.ID, &u.Username, &u.Email, &disabled, &hash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Constant-time compare with a placeholder hash so the timing of
		// "no such user" looks like "wrong password". This is a tripwire,
		// not a perfect side-channel mitigation.
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$............................................................"),
			[]byte(password))
		return User{}, errors.New("invalid credentials")
	}
	if err != nil {
		return User{}, err
	}
	if disabled != 0 {
		return User{}, errors.New("account disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return User{}, errors.New("invalid credentials")
	}
	u.Disabled = false
	return u, nil
}

// CreateSession issues a fresh session token for u with the given TTL.
func (r *Repo) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	expiry := time.Now().Add(ttl)
	if _, err := r.DB.ExecContext(ctx,
		`INSERT INTO user_session (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expiry); err != nil {
		return "", err
	}
	return token, nil
}

// LookupSession returns the user_id for a session token if it exists and
// has not expired. Empty token returns (0, error).
func (r *Repo) LookupSession(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, errors.New("no session")
	}
	var userID int64
	var expires time.Time
	err := r.DB.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM user_session WHERE token=?`,
		token).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("invalid session")
	}
	if err != nil {
		return 0, err
	}
	if time.Now().After(expires) {
		_, _ = r.DB.ExecContext(ctx, `DELETE FROM user_session WHERE token=?`, token)
		return 0, errors.New("session expired")
	}
	return userID, nil
}

// Session lifetime. A session expires after SessionIdleTimeout without
// activity: each authenticated request (TouchSession) pushes expires_at
// forward again, but never past SessionMaxLifetime from sign-in
// (created_at). Sessions issued before sliding expiry keep their stored
// expires_at and simply start sliding on their next request.
var (
	SessionIdleTimeout = 12 * time.Hour
	SessionMaxLifetime = 7 * 24 * time.Hour
	// sessionTouchEvery throttles the sliding update to one write per
	// session per interval, however busy the operator is.
	sessionTouchEvery = time.Minute
)

// SessionInfo describes a live session.
type SessionInfo struct {
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	// Extended reports that TouchSession moved ExpiresAt forward on this
	// call (so the caller can refresh the cookie).
	Extended bool
}

// SessionDeadline is the hard cap for a session created at created.
func SessionDeadline(created time.Time) time.Time {
	return created.Add(SessionMaxLifetime)
}

// TouchSession validates token like LookupSession and records activity:
// the expiry slides to now+SessionIdleTimeout (capped at the session's
// absolute deadline), written at most once per sessionTouchEvery. An
// expired session is deleted and reported as an error.
func (r *Repo) TouchSession(ctx context.Context, token string) (SessionInfo, error) {
	return r.touchSession(ctx, token, sessionTouchEvery)
}

// ExtendSession is TouchSession without the throttle, for an explicit
// "Stay signed in": the expiry always moves to the full idle window.
func (r *Repo) ExtendSession(ctx context.Context, token string) (SessionInfo, error) {
	return r.touchSession(ctx, token, time.Nanosecond)
}

func (r *Repo) touchSession(ctx context.Context, token string, every time.Duration) (SessionInfo, error) {
	info, err := r.lookupSessionInfo(ctx, token)
	if err != nil {
		return SessionInfo{}, err
	}
	now := time.Now()
	next := now.Add(SessionIdleTimeout)
	if !info.CreatedAt.IsZero() {
		if dl := SessionDeadline(info.CreatedAt); next.After(dl) {
			next = dl
		}
	}
	if next.Sub(info.ExpiresAt) >= every {
		if _, err := r.DB.ExecContext(ctx,
			`UPDATE user_session SET expires_at=? WHERE token=?`, next, token); err != nil {
			return SessionInfo{}, err
		}
		info.ExpiresAt = next
		info.Extended = true
	}
	return info, nil
}

// SessionStatus validates token without recording activity, for
// background requests (polling) that mustn't keep an idle session alive.
func (r *Repo) SessionStatus(ctx context.Context, token string) (SessionInfo, error) {
	return r.lookupSessionInfo(ctx, token)
}

func (r *Repo) lookupSessionInfo(ctx context.Context, token string) (SessionInfo, error) {
	if token == "" {
		return SessionInfo{}, errors.New("no session")
	}
	var info SessionInfo
	var created sql.NullTime
	err := r.DB.QueryRowContext(ctx,
		`SELECT user_id, created_at, expires_at FROM user_session WHERE token=?`,
		token).Scan(&info.UserID, &created, &info.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionInfo{}, errors.New("invalid session")
	}
	if err != nil {
		return SessionInfo{}, err
	}
	if created.Valid {
		info.CreatedAt = created.Time
	}
	if time.Now().After(info.ExpiresAt) {
		_, _ = r.DB.ExecContext(ctx, `DELETE FROM user_session WHERE token=?`, token)
		return SessionInfo{}, errors.New("session expired")
	}
	return info, nil
}

// DeleteSession revokes a session token (logout).
func (r *Repo) DeleteSession(ctx context.Context, token string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM user_session WHERE token=?`, token)
	return err
}

// PruneExpiredSessions removes all sessions whose expiry has passed.
func (r *Repo) PruneExpiredSessions(ctx context.Context) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM user_session WHERE expires_at < ?`, time.Now())
	return err
}

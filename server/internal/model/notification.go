package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Notification is a single in-portal notification for a user.
type Notification struct {
	ID         ID         `json:"id"`
	UserID     int64      `json:"user_id"`
	OccurredAt time.Time  `json:"occurred_at"`
	Event      string     `json:"event"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Link       string     `json:"link"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
}

// NotificationPreference controls what a user receives per event.
type NotificationPreference struct {
	ID     ID     `json:"id"`
	UserID int64  `json:"user_id"`
	Event  string `json:"event"`
	Portal bool   `json:"portal"`
	Email  bool   `json:"email"`
}

// NotificationRepo handles notification CRUD.
type NotificationRepo struct{ db *storage.DB }

func NewNotificationRepo(db *storage.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

// Insert creates a notification.
func (r *NotificationRepo) Insert(ctx context.Context, n Notification) error {
	if strings.TrimSpace(n.Title) == "" {
		return fmt.Errorf("%w: notification title required", ErrValidation)
	}
	when := n.OccurredAt
	if when.IsZero() {
		when = time.Now()
	}
	if n.Severity == "" {
		n.Severity = "info"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO notification (user_id, occurred_at, event, severity, title, body, link)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.UserID, when, n.Event, n.Severity, n.Title, n.Body, n.Link)
	return err
}

// UnreadCount returns how many unread notifications a user has.
func (r *NotificationRepo) UnreadCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notification WHERE user_id=? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// NotificationSearch controls listing.
type NotificationSearch struct {
	UserID   int64
	Severity string
	Event    string
	Unread   bool
	Limit    int
	Offset   int
}

// List returns notifications for the search criteria, newest first.
func (r *NotificationRepo) List(ctx context.Context, s NotificationSearch) ([]Notification, int, error) {
	var where []string
	var args []any
	where = append(where, "user_id=?")
	args = append(args, s.UserID)
	if s.Severity != "" {
		where = append(where, "severity=?")
		args = append(args, s.Severity)
	}
	if s.Event != "" {
		where = append(where, "event=?")
		args = append(args, s.Event)
	}
	if s.Unread {
		where = append(where, "read_at IS NULL")
	}
	clause := "WHERE " + strings.Join(where, " AND ")

	var total int
	countQ := "SELECT COUNT(*) FROM notification " + clause
	if err := r.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := s.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, user_id, occurred_at, event, severity, title, body, link, read_at
	      FROM notification ` + clause + ` ORDER BY occurred_at DESC LIMIT ? OFFSET ?`
	allArgs := append(args, limit, s.Offset)
	rows, err := r.db.QueryContext(ctx, q, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var v Notification
		var occurred, readAt sql.NullString
		if err := rows.Scan(&v.ID, &v.UserID, &occurred, &v.Event, &v.Severity,
			&v.Title, &v.Body, &v.Link, &readAt); err != nil {
			return nil, 0, err
		}
		v.OccurredAt = parseDBTime(occurred.String)
		if readAt.Valid {
			t := parseDBTime(readAt.String)
			v.ReadAt = &t
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// MarkRead marks specific notifications as read.
func (r *NotificationRepo) MarkRead(ctx context.Context, userID int64, ids []ID) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := []any{time.Now(), userID}
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE notification SET read_at=? WHERE user_id=? AND id IN (`+
		strings.Join(placeholders, ",")+`)`, args...)
	return err
}

// MarkAllRead marks all of a user's notifications as read.
func (r *NotificationRepo) MarkAllRead(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE notification SET read_at=? WHERE user_id=? AND read_at IS NULL`,
		time.Now(), userID)
	return err
}

// Prune deletes notifications older than cutoff.
func (r *NotificationRepo) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM notification WHERE occurred_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetPreferences returns all preferences for a user.
func (r *NotificationRepo) GetPreferences(ctx context.Context, userID int64) ([]NotificationPreference, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, event, portal, email FROM notification_preference WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationPreference
	for rows.Next() {
		var v NotificationPreference
		if err := rows.Scan(&v.ID, &v.UserID, &v.Event, &v.Portal, &v.Email); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetPreference upserts a preference for a user + event.
func (r *NotificationRepo) SetPreference(ctx context.Context, userID int64, event string, portal, email bool) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO notification_preference (user_id, event, portal, email)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, event) DO UPDATE SET portal=excluded.portal, email=excluded.email`,
		userID, event, portal, email)
	return err
}

// WantsPortal returns true if the user has portal notifications enabled
// for the given event (defaults to true if no preference row exists).
func (r *NotificationRepo) WantsPortal(ctx context.Context, userID int64, event string) bool {
	var portal int
	err := r.db.QueryRowContext(ctx,
		`SELECT portal FROM notification_preference WHERE user_id=? AND event=?`,
		userID, event).Scan(&portal)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return portal == 1
}

// WantsEmail returns true if the user has email notifications enabled for
// the given event (defaults to false if no preference row exists).
func (r *NotificationRepo) WantsEmail(ctx context.Context, userID int64, event string) bool {
	var email int
	err := r.db.QueryRowContext(ctx,
		`SELECT email FROM notification_preference WHERE user_id=? AND event=?`,
		userID, event).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return email == 1
}

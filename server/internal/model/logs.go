package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// LogEvent is one log row. Fields is a flat JSON object of extra
// attributes the emitter attached.
type LogEvent struct {
	ID         ID        `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Component  string    `json:"component"`
	Level      string    `json:"level"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Fields     string    `json:"fields"`
}

// LogRepo writes and reads log events.
type LogRepo struct{ db *storage.DB }

func NewLogRepo(db *storage.DB) *LogRepo { return &LogRepo{db: db} }

// Append inserts an event. Empty action is rejected; fields must be
// valid JSON (defaults to "{}" if empty).
func (r *LogRepo) Append(ctx context.Context, ev LogEvent) error {
	if strings.TrimSpace(ev.Action) == "" {
		return fmt.Errorf("%w: log action required", ErrValidation)
	}
	if ev.Level == "" {
		ev.Level = "INFO"
	}
	if ev.Fields == "" {
		ev.Fields = "{}"
	}
	if !json.Valid([]byte(ev.Fields)) {
		return fmt.Errorf("%w: log fields must be valid JSON", ErrValidation)
	}
	when := ev.OccurredAt
	if when.IsZero() {
		when = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO log_event (occurred_at, component, level, actor, action, target, fields)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		when, ev.Component, ev.Level, ev.Actor, ev.Action, ev.Target, ev.Fields)
	return err
}

// AppendBatch inserts many events in one transaction.
func (r *LogRepo) AppendBatch(ctx context.Context, evs []LogEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO log_event (occurred_at, component, level, actor, action, target, fields)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ev := range evs {
		when := ev.OccurredAt
		if when.IsZero() {
			when = time.Now()
		}
		if ev.Level == "" {
			ev.Level = "INFO"
		}
		if ev.Fields == "" {
			ev.Fields = "{}"
		}
		if _, err := stmt.ExecContext(ctx, when, ev.Component, ev.Level,
			ev.Actor, ev.Action, ev.Target, ev.Fields); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Search returns events matching the given filters, newest first, up to
// limit rows. Empty filter strings are ignored.
type LogSearch struct {
	Component string
	Actor     string
	Action    string
	Since     time.Time
	Until     time.Time
	Limit     int
}

func (r *LogRepo) Search(ctx context.Context, s LogSearch) ([]LogEvent, error) {
	var (
		where []string
		args  []any
	)
	if s.Component != "" {
		where = append(where, "component=?")
		args = append(args, s.Component)
	}
	if s.Actor != "" {
		where = append(where, "actor=?")
		args = append(args, s.Actor)
	}
	if s.Action != "" {
		where = append(where, "action=?")
		args = append(args, s.Action)
	}
	if !s.Since.IsZero() {
		where = append(where, "occurred_at>=?")
		args = append(args, s.Since)
	}
	if !s.Until.IsZero() {
		where = append(where, "occurred_at<=?")
		args = append(args, s.Until)
	}
	q := `SELECT id, occurred_at, component, level, actor, action, target, fields
	      FROM log_event`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY occurred_at DESC LIMIT ?"
	limit := s.Limit
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEvent
	for rows.Next() {
		var v LogEvent
		if err := rows.Scan(&v.ID, &v.OccurredAt, &v.Component, &v.Level,
			&v.Actor, &v.Action, &v.Target, &v.Fields); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Prune deletes events older than cutoff. Returns the number removed.
func (r *LogRepo) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM log_event WHERE occurred_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

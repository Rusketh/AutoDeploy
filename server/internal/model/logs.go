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
	// MachineName is the human-readable name resolved from Actor (an agent's
	// SMBIOS UUID) at read time. Never persisted; populated by
	// EnrichMachineNames so the portal and the live-tail JSON can show a name
	// instead of a raw UUID. Empty for non-machine actors (system/operator).
	MachineName string `json:"machine_name,omitempty"`
}

// EnrichMachineNames fills MachineName on each event whose Actor (the agent's
// system UUID) resolves to a name in the supplied map. Build the map once per
// request with InventoryRepo.NamesByUUID. Events with an unknown or
// non-machine actor are left untouched.
func EnrichMachineNames(events []LogEvent, names map[string]string) {
	if len(names) == 0 {
		return
	}
	for i := range events {
		if n := names[events[i].Actor]; n != "" {
			events[i].MachineName = n
		}
	}
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

// appendBatchChunkSize is the maximum number of events inserted per
// transaction in AppendBatch, keeping the write-lock hold time bounded.
const appendBatchChunkSize = 500

// AppendBatch inserts many events, splitting them into chunks of at most
// appendBatchChunkSize records so that no single transaction holds the
// write lock for an unbounded duration.
func (r *LogRepo) AppendBatch(ctx context.Context, evs []LogEvent) error {
	if len(evs) == 0 {
		return nil
	}
	for start := 0; start < len(evs); start += appendBatchChunkSize {
		end := start + appendBatchChunkSize
		if end > len(evs) {
			end = len(evs)
		}
		if err := r.appendBatchChunk(ctx, evs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// appendBatchChunk inserts a single chunk of events in one transaction.
func (r *LogRepo) appendBatchChunk(ctx context.Context, evs []LogEvent) error {
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
		var ts scanTime
		if err := rows.Scan(&v.ID, &ts, &v.Component, &v.Level,
			&v.Actor, &v.Action, &v.Target, &v.Fields); err != nil {
			return nil, err
		}
		v.OccurredAt = ts.Time
		out = append(out, v)
	}
	return out, rows.Err()
}

// scanTime reads a timestamp column tolerantly. The pure-Go SQLite driver
// only auto-parses a narrow set of DATETIME layouts; for any other stored
// string (or an empty value) it hands back the raw string, which then fails
// a direct scan into *time.Time with "unsupported Scan". Routing occurred_at
// through scanTime keeps the Logs page working regardless of how a row's
// timestamp was written.
type scanTime struct{ Time time.Time }

func (s *scanTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		s.Time = time.Time{}
	case time.Time:
		s.Time = x
	case []byte:
		s.Time = parseDBTime(string(x))
	case string:
		s.Time = parseDBTime(x)
	case int64:
		s.Time = time.Unix(x, 0).UTC()
	default:
		return fmt.Errorf("unsupported timestamp value of type %T", v)
	}
	return nil
}

// dbTimeLayouts are the formats a stored timestamp might appear in: the Go
// driver's own DATETIME forms, RFC 3339 (what clients ship), the SQLite text
// default from CURRENT_TIMESTAMP, and Go's time.Time.String() output.
var dbTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

// parseDBTime turns a stored timestamp string into a time.Time, trying each
// known layout. An empty or unrecognised value yields the zero time rather
// than failing the whole query — one odd row should not break the page.
func parseDBTime(str string) time.Time {
	str = strings.TrimSpace(str)
	if str == "" {
		return time.Time{}
	}
	for _, layout := range dbTimeLayouts {
		if t, err := time.Parse(layout, str); err == nil {
			return t
		}
	}
	return time.Time{}
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

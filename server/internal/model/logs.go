package model

import (
	"context"
	"database/sql"
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

	// The columns below were added by migration 0035. All are optional:
	// rows written earlier hold NULL (read back as ""/0) and the read path
	// (NameLookup.DescribeLogs) fills Message/Hint/MachineID from the
	// catalogue and the actor instead. Clients never need to send them; the
	// server derives them at ingest.

	// Message is the plain-English sentence for the row.
	Message string `json:"message,omitempty"`
	// MachineID is the machine_record the event belongs to (0 = none known).
	MachineID ID `json:"machine_id,omitempty"`
	// DeploymentID is the deployment_history row of the run (0 = none).
	DeploymentID ID `json:"deployment_id,omitempty"`
	// User is the portal user behind an audit event.
	User string `json:"user,omitempty"`
	// Outcome is "ok" / "failed" when the event reports a result.
	Outcome string `json:"outcome,omitempty"`
	// Error is the low-level error text (from fields.error).
	Error string `json:"error,omitempty"`
	// Hint is what to try next, for failures the catalogue knows.
	Hint string `json:"hint,omitempty"`

	// Category is the catalogue category ("Deployment", "Software", …).
	// Read-time only, set by DescribeLogs.
	Category string `json:"category,omitempty"`
	// MachineName is the human-readable name resolved from Actor (an agent's
	// SMBIOS UUID) at read time. Never persisted; populated by
	// EnrichMachineNames so the portal and the live-tail JSON can show a name
	// instead of a raw UUID. Empty for non-machine actors (system/operator).
	MachineName string `json:"machine_name,omitempty"`
	// TargetName is the display name of the entity Target refers to
	// ("machine:12" -> "LAB-01", "software/3" -> "7-Zip", "#3 (deleted)" for a
	// row that no longer exists). Read-time only, set by
	// NameLookup.EnrichLogs; empty when Target names nothing resolvable.
	TargetName string `json:"target_name,omitempty"`
	// TargetKind / TargetID are the parsed Target (a Kind* constant and row
	// ID) so the portal can link it. Not part of the JSON shape.
	TargetKind string `json:"-"`
	TargetID   ID     `json:"-"`
}

// EnrichMachineNames fills MachineName on each event whose Actor (the agent's
// system UUID) resolves to a name in the supplied map. Build the map once per
// request with InventoryRepo.NamesByUUID. Events with an unknown or
// non-machine actor are left untouched. NameLookup.EnrichLogs supersedes this
// (it also resolves agent-ID actors and targets).
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

const logInsertSQL = `
		INSERT INTO log_event (occurred_at, component, level, actor, action, target, fields,
			message, machine_id, deployment_id, user, outcome, error, hint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// logInsertArgs normalises an event and returns its insert arguments. The
// optional 0035 columns are written as NULL when empty so "unknown" stays
// distinguishable from a real value.
func logInsertArgs(ev LogEvent) []any {
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
	return []any{when, ev.Component, ev.Level, ev.Actor, ev.Action, ev.Target, ev.Fields,
		nullStr(ev.Message), logNullID(ev.MachineID), logNullID(ev.DeploymentID), nullStr(ev.User),
		nullStr(ev.Outcome), nullStr(ev.Error), nullStr(ev.Hint)}
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func logNullID(id ID) any {
	if id <= 0 {
		return nil
	}
	return int64(id)
}

// Append inserts an event. Empty action is rejected; fields must be
// valid JSON (defaults to "{}" if empty).
func (r *LogRepo) Append(ctx context.Context, ev LogEvent) error {
	if strings.TrimSpace(ev.Action) == "" {
		return fmt.Errorf("%w: log action required", ErrValidation)
	}
	if ev.Fields != "" && !json.Valid([]byte(ev.Fields)) {
		return fmt.Errorf("%w: log fields must be valid JSON", ErrValidation)
	}
	_, err := r.db.ExecContext(ctx, logInsertSQL, logInsertArgs(ev)...)
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
	stmt, err := tx.PrepareContext(ctx, logInsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ev := range evs {
		if _, err := stmt.ExecContext(ctx, logInsertArgs(ev)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LogSearch is a set of log filters. Empty / zero filters are ignored; all
// given filters must match.
type LogSearch struct {
	Component string
	Actor     string
	// Actors restricts results to events whose actor is one of these values
	// (an `actor IN (...)` clause). Used to filter by a machine group: resolve
	// the group to its members' SMBIOS UUIDs and pass them here. An empty slice
	// is ignored; a non-empty Actors takes precedence over the single Actor.
	Actors []string
	Action string
	Since  time.Time
	Until  time.Time
	Limit  int

	// Levels keeps events whose effective level (after the catalogue's
	// overrides, e.g. old WARN stderr lines count as INFO) is one of these.
	Levels []string
	// Machine restricts to one machine: rows tagged with its machine_id, plus
	// untagged (older) rows whose actor is one of its identifiers or whose
	// target names it.
	Machine *LogMachine
	// DeploymentID restricts to one deployment run: rows tagged with it, plus
	// untagged rows from the run's machine inside the run's time window
	// (Window), so runs logged by older clients still show up.
	DeploymentID ID
	Window       *LogWindow
	// User matches the audit user (or, on older audit rows, the actor).
	User string
	// Category is a catalogue category (LogCat*).
	Category string
	// Text matches message, action, target, actor and the fields JSON, plus
	// actions whose catalogue message contains it.
	Text string
	// BeforeID / AfterID page by row ID (cursor paging). Either one, or
	// ByID, orders the result by id DESC instead of occurred_at DESC.
	BeforeID ID
	AfterID  ID
	ByID     bool
}

// LogMachine identifies one machine for LogSearch.Machine.
type LogMachine struct {
	ID ID
	// Actors are the machine's SMBIOS UUID and agent ID (whichever are set).
	Actors []string
}

// LogWindow is a deployment run's time span for LogSearch.Window.
type LogWindow struct {
	From, To time.Time
	Machine  LogMachine
}

// machineClause matches rows for m: tagged, or untagged with a matching
// actor / "machine:N" target.
func machineClause(m LogMachine) (string, []any) {
	var alt []string
	var args []any
	alt = append(alt, "target IN (?, ?)")
	args = append(args, fmt.Sprintf("machine:%d", int64(m.ID)), fmt.Sprintf("machine/%d", int64(m.ID)))
	for _, a := range m.Actors {
		if strings.TrimSpace(a) == "" {
			continue
		}
		alt = append(alt, "actor=?", "target=?")
		args = append(args, a, "machine:"+a)
	}
	q := "(machine_id=? OR (machine_id IS NULL AND (" + strings.Join(alt, " OR ") + ")))"
	return q, append([]any{int64(m.ID)}, args...)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func strArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// levelClause matches events whose effective level is in levels: the
// catalogue's level overrides win over the stored level.
func levelClause(levels []string) (string, []any) {
	ov := logLevelOverrides()
	var overridden []string
	for _, acts := range ov {
		overridden = append(overridden, acts...)
	}
	var alts []string
	var args []any
	var want []string
	for _, l := range levels {
		l = normLevel(l)
		want = append(want, l)
		if acts := ov[l]; len(acts) > 0 {
			alts = append(alts, "action IN ("+placeholders(len(acts))+")")
			args = append(args, strArgs(acts)...)
		}
	}
	stored := "level IN (" + placeholders(len(want)) + ")"
	args2 := strArgs(want)
	if len(overridden) > 0 {
		stored = "(" + stored + " AND action NOT IN (" + placeholders(len(overridden)) + "))"
		args2 = append(args2, strArgs(overridden)...)
	}
	alts = append([]string{stored}, alts...)
	return "(" + strings.Join(alts, " OR ") + ")", append(args2, args...)
}

// categoryClause matches events in catalogue category cat: catalogued
// actions of that category, plus uncatalogued actions whose first segment
// maps to it (and, for System, uncatalogued actions no prefix claims).
func categoryClause(cat string) (string, []any) {
	exact, prefixes := logCategoryActions(cat)
	all := make([]string, 0, len(logCatalog))
	for a := range logCatalog {
		all = append(all, a)
	}
	var alts []string
	var args []any
	if len(exact) > 0 {
		alts = append(alts, "action IN ("+placeholders(len(exact))+")")
		args = append(args, strArgs(exact)...)
	}
	notCatalogued := "action NOT IN (" + placeholders(len(all)) + ")"
	if cat == LogCatSystem {
		// Anything uncatalogued that no other category's prefix claims.
		var claimed []string
		for p, c := range logPrefixCategories {
			if c != LogCatSystem {
				claimed = append(claimed, p)
			}
		}
		var nots []string
		var nargs []any
		for _, p := range claimed {
			nots = append(nots, "action NOT LIKE ? ESCAPE '\\'", "action NOT LIKE ? ESCAPE '\\'", "action<>?")
			nargs = append(nargs, likeEscape(p)+".%", likeEscape(p)+"\\_%", p)
		}
		alts = append(alts, "("+notCatalogued+" AND "+strings.Join(nots, " AND ")+")")
		args = append(args, strArgs(all)...)
		args = append(args, nargs...)
	} else if len(prefixes) > 0 {
		var likes []string
		var largs []any
		for _, p := range prefixes {
			likes = append(likes, "action LIKE ? ESCAPE '\\'", "action LIKE ? ESCAPE '\\'", "action=?")
			largs = append(largs, likeEscape(p)+".%", likeEscape(p)+"\\_%", p)
		}
		alts = append(alts, "("+notCatalogued+" AND ("+strings.Join(likes, " OR ")+"))")
		args = append(args, strArgs(all)...)
		args = append(args, largs...)
	}
	if len(alts) == 0 {
		return "0", nil
	}
	return "(" + strings.Join(alts, " OR ") + ")", args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// where builds the WHERE clause (without the keyword) and its args.
func (s LogSearch) where() ([]string, []any) {
	var (
		where []string
		args  []any
	)
	if s.Component != "" {
		where = append(where, "component=?")
		args = append(args, s.Component)
	}
	if len(s.Actors) > 0 {
		where = append(where, "actor IN ("+placeholders(len(s.Actors))+")")
		args = append(args, strArgs(s.Actors)...)
	} else if s.Actor != "" {
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
	if len(s.Levels) > 0 {
		q, a := levelClause(s.Levels)
		where = append(where, q)
		args = append(args, a...)
	}
	if s.Machine != nil {
		q, a := machineClause(*s.Machine)
		where = append(where, q)
		args = append(args, a...)
	}
	if s.DeploymentID > 0 {
		if s.Window != nil {
			mq, ma := machineClause(s.Window.Machine)
			where = append(where, "(deployment_id=? OR (deployment_id IS NULL AND occurred_at>=? AND occurred_at<=? AND "+mq+"))")
			args = append(args, int64(s.DeploymentID), s.Window.From, s.Window.To)
			args = append(args, ma...)
		} else {
			where = append(where, "deployment_id=?")
			args = append(args, int64(s.DeploymentID))
		}
	}
	if u := strings.TrimSpace(s.User); u != "" {
		where = append(where, "(user=? OR (user IS NULL AND actor=?))")
		args = append(args, u, u)
	}
	if s.Category != "" {
		q, a := categoryClause(s.Category)
		where = append(where, q)
		args = append(args, a...)
	}
	if t := strings.TrimSpace(s.Text); t != "" {
		like := "%" + likeEscape(t) + "%"
		alts := []string{
			"message LIKE ? ESCAPE '\\'", "action LIKE ? ESCAPE '\\'", "target LIKE ? ESCAPE '\\'",
			"actor LIKE ? ESCAPE '\\'", "fields LIKE ? ESCAPE '\\'", "error LIKE ? ESCAPE '\\'",
		}
		a := []any{like, like, like, like, like, like}
		if acts := LogActionsMatchingText(t); len(acts) > 0 {
			// Old rows have no stored message; match their catalogue text.
			alts = append(alts, "(message IS NULL AND action IN ("+placeholders(len(acts))+"))")
			a = append(a, strArgs(acts)...)
		}
		where = append(where, "("+strings.Join(alts, " OR ")+")")
		args = append(args, a...)
	}
	return where, args
}

const logSelectCols = `id, occurred_at, component, level, actor, action, target, fields,
	message, machine_id, deployment_id, user, outcome, error, hint`

// Search returns events matching the given filters, newest first, up to
// limit rows.
func (r *LogRepo) Search(ctx context.Context, s LogSearch) ([]LogEvent, error) {
	return r.search(ctx, s, false)
}

// SearchOldestFirst reads the limit rows just above s.AfterID (oldest
// first) and returns them newest first: the "Newer" page of a cursor walk.
func (r *LogRepo) SearchOldestFirst(ctx context.Context, s LogSearch) ([]LogEvent, error) {
	out, err := r.search(ctx, s, true)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

func (r *LogRepo) search(ctx context.Context, s LogSearch, ascending bool) ([]LogEvent, error) {
	where, args := s.where()
	if s.BeforeID > 0 {
		where = append(where, "id<?")
		args = append(args, int64(s.BeforeID))
	}
	if s.AfterID > 0 {
		where = append(where, "id>?")
		args = append(args, int64(s.AfterID))
	}
	q := `SELECT ` + logSelectCols + ` FROM log_event`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if ascending {
		q += " ORDER BY id ASC LIMIT ?"
	} else if s.ByID || s.BeforeID > 0 || s.AfterID > 0 {
		q += " ORDER BY id DESC LIMIT ?"
	} else {
		q += " ORDER BY occurred_at DESC LIMIT ?"
	}
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
		var msg, user, outcome, errText, hint sql.NullString
		var mid, did sql.NullInt64
		if err := rows.Scan(&v.ID, &ts, &v.Component, &v.Level,
			&v.Actor, &v.Action, &v.Target, &v.Fields,
			&msg, &mid, &did, &user, &outcome, &errText, &hint); err != nil {
			return nil, err
		}
		v.OccurredAt = ts.Time
		v.Message, v.User, v.Outcome, v.Error, v.Hint = msg.String, user.String, outcome.String, errText.String, hint.String
		v.MachineID, v.DeploymentID = ID(mid.Int64), ID(did.Int64)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Count returns how many events match s (ignoring paging and limit).
func (r *LogRepo) Count(ctx context.Context, s LogSearch) (int, error) {
	where, args := s.where()
	q := `SELECT COUNT(*) FROM log_event`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	err := r.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// CountNewer returns how many matching events are newer than id — the
// position of a page in the whole result ("Showing 201–400 of N").
func (r *LogRepo) CountNewer(ctx context.Context, s LogSearch, id ID) (int, error) {
	where, args := s.where()
	where = append(where, "id>=?")
	args = append(args, int64(id))
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_event WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	return n, err
}

// Users returns the distinct audit users seen in the log (for the filter's
// suggestions), most recent first, capped at 100.
func (r *LogRepo) Users(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user FROM log_event WHERE user IS NOT NULL AND user<>''
		GROUP BY user ORDER BY MAX(id) DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
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

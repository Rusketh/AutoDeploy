package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// LogQueryLayout is the datetime-local format the Logs page's From/To fields
// use (interpreted in the configured display time zone).
const LogQueryLayout = "2006-01-02T15:04"

// DefaultLogPageSize is how many rows one Logs page / API call returns when
// no limit is given.
const DefaultLogPageSize = 200

// LogQuery is a parsed Logs search: the repo filter plus what the page needs
// to echo the form and explain the result.
type LogQuery struct {
	Search LogSearch

	// Echoed form values (as typed, so the form keeps them after searching).
	Level, Machine, User, Category, Deployment, From, To, Text string

	// MachineRec is the machine the "machine" filter resolved to.
	MachineRec *MachineRecord
	// MachineUnknown is set when the machine filter matched no machine (the
	// search then falls back to matching the text as an actor).
	MachineUnknown bool
	// DeploymentRec is the run the "deployment" filter names.
	DeploymentRec *DeploymentRecord
	// Active reports whether any filter is set.
	Active bool
}

// LogLevelChoices are the level filter's options (value, label).
var LogLevelChoices = [][2]string{
	{"", "Any level"},
	{"problems", "Warnings and errors"},
	{"ERROR", "Errors only"},
	{"WARN", "Warnings only"},
	{"INFO", "Info only"},
}

// ParseLogQuery turns Logs query parameters into a search. It accepts the
// current parameters (level, machine, user, category, deployment, from, to,
// q, before, after) and every older one (component, actor, action, since,
// until, limit) so existing bookmarks keep working. Times in from/to are
// datetime-local values in loc; since/until accept RFC 3339, or the old
// datetime-local form read as UTC (which is what the old page sent).
// inv resolves the machine and deployment filters; it may be nil.
func ParseLogQuery(ctx context.Context, v url.Values, inv *InventoryRepo, loc *time.Location) LogQuery {
	if loc == nil {
		loc = time.UTC
	}
	get := func(k string) string { return strings.TrimSpace(v.Get(k)) }
	q := LogQuery{
		Level: get("level"), Machine: get("machine"), User: get("user"),
		Category: get("category"), Deployment: get("deployment"),
		From: get("from"), To: get("to"), Text: get("q"),
	}
	s := LogSearch{
		Component: get("component"),
		Actor:     get("actor"),
		Action:    get("action"),
		User:      q.User,
		Text:      q.Text,
		Limit:     DefaultLogPageSize,
	}
	if n, err := strconv.Atoi(get("limit")); err == nil && n > 0 {
		s.Limit = n
	}
	if t, ok := parseOldLogTime(get("since")); ok {
		s.Since = t
	}
	if t, ok := parseOldLogTime(get("until")); ok {
		s.Until = t
	}
	if t, err := time.ParseInLocation(LogQueryLayout, q.From, loc); err == nil {
		s.Since = t
	}
	if t, err := time.ParseInLocation(LogQueryLayout, q.To, loc); err == nil {
		// "To 14:05" includes everything logged during that minute.
		s.Until = t.Add(time.Minute - time.Nanosecond)
	}
	switch strings.ToLower(q.Level) {
	case "":
	case "problems":
		s.Levels = []string{"WARN", "ERROR"}
	default:
		for _, l := range strings.Split(q.Level, ",") {
			if l = strings.TrimSpace(l); l != "" {
				s.Levels = append(s.Levels, normLevel(l))
			}
		}
	}
	for _, c := range LogCategories {
		if strings.EqualFold(c, q.Category) {
			s.Category = c
			q.Category = c
		}
	}
	if q.Machine != "" {
		if m, ok := resolveLogMachine(ctx, inv, q.Machine); ok {
			q.MachineRec = &m
			lm := LogMachineFor(m)
			s.Machine = &lm
		} else {
			// Unknown: still filter (an actor that never enrolled, or a typo
			// that should produce an empty result rather than everything).
			q.MachineUnknown = true
			s.Machine = &LogMachine{ID: -1, Actors: []string{q.Machine}}
		}
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(q.Deployment, "#"), 10, 64); err == nil && n > 0 {
		s.DeploymentID = ID(n)
		if inv != nil {
			if d, err := inv.GetDeployment(ctx, ID(n)); err == nil {
				q.DeploymentRec = &d
				if m, err := inv.Get(ctx, d.MachineID); err == nil {
					end := time.Now()
					if d.CompletedAt != nil {
						end = *d.CompletedAt
					}
					// Older clients ship their logs at the end of a run, with
					// timestamps from the machine's clock: allow some slack.
					s.Window = &LogWindow{
						From:    d.StartedAt.Add(-10 * time.Minute),
						To:      end.Add(15 * time.Minute),
						Machine: LogMachineFor(m),
					}
				}
			}
		}
	}
	if n, err := strconv.ParseInt(get("before"), 10, 64); err == nil && n > 0 {
		s.BeforeID = ID(n)
	}
	if n, err := strconv.ParseInt(get("after"), 10, 64); err == nil && n > 0 {
		s.AfterID = ID(n)
	} else if n, err := strconv.ParseInt(get("after_id"), 10, 64); err == nil && n > 0 {
		s.AfterID = ID(n)
	}
	q.Search = s
	q.Active = s.Component != "" || s.Actor != "" || s.Action != "" || s.User != "" || s.Text != "" ||
		!s.Since.IsZero() || !s.Until.IsZero() || len(s.Levels) > 0 || s.Category != "" ||
		s.Machine != nil || s.DeploymentID > 0
	return q
}

func parseOldLogTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.Parse(LogQueryLayout, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// resolveLogMachine finds a machine by row ID, SMBIOS UUID, agent ID or
// display / reported name (case-insensitive).
func resolveLogMachine(ctx context.Context, inv *InventoryRepo, ref string) (MachineRecord, bool) {
	if inv == nil {
		return MachineRecord{}, false
	}
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "#"))
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil && n > 0 {
		if m, err := inv.Get(ctx, ID(n)); err == nil {
			return m, true
		}
	}
	if id, err := inv.MachineIDByActor(ctx, ref); err == nil && id > 0 {
		if m, err := inv.Get(ctx, id); err == nil {
			return m, true
		}
	}
	idx, err := inv.NameIndex(ctx)
	if err != nil {
		return MachineRecord{}, false
	}
	for id, n := range idx.ByID {
		if strings.EqualFold(n.Name, ref) || (n.RenamingFrom != "" && strings.EqualFold(n.RenamingFrom, ref)) {
			if m, err := inv.Get(ctx, id); err == nil {
				return m, true
			}
		}
	}
	return MachineRecord{}, false
}

// MachineIDByActor resolves an SMBIOS UUID (either case) or agent ID to the
// machine's row ID with one indexed query. ErrNotFound when unknown.
func (r *InventoryRepo) MachineIDByActor(ctx context.Context, actor string) (ID, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return 0, fmt.Errorf("actor: %w", ErrNotFound)
	}
	var id ID
	err := r.db.QueryRowContext(ctx, `
		SELECT id FROM machine_record
		WHERE system_uuid IN (?, ?) OR (agent_id<>'' AND agent_id=?) LIMIT 1`,
		actor, strings.ToUpper(actor), actor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("machine %s: %w", actor, ErrNotFound)
	}
	return id, err
}

// FailedLoginLogsURL is the Logs view that lists failed sign-ins for one
// attempted username (where the failed-login notification links to).
func FailedLoginLogsURL(username string) string {
	v := url.Values{}
	v.Set("action", "auth.login.fail")
	if username != "" && username != "(blank)" {
		v.Set("q", username)
	}
	return "/portal/logs?" + v.Encode()
}

// Values renders the query back to URL parameters (new-style names only),
// for links such as the pager and the live tail.
func (q LogQuery) Values() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	s := q.Search
	set("level", q.Level)
	set("machine", q.Machine)
	set("user", q.User)
	set("category", q.Category)
	set("deployment", q.Deployment)
	set("from", q.From)
	set("to", q.To)
	set("q", q.Text)
	set("component", s.Component)
	set("actor", s.Actor)
	set("action", s.Action)
	if q.From == "" && !s.Since.IsZero() {
		v.Set("since", s.Since.UTC().Format(time.RFC3339))
	}
	if q.To == "" && !s.Until.IsZero() {
		v.Set("until", s.Until.UTC().Format(time.RFC3339))
	}
	if s.Limit > 0 && s.Limit != DefaultLogPageSize {
		v.Set("limit", strconv.Itoa(s.Limit))
	}
	return v
}

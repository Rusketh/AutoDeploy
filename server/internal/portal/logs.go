package portal

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerLogsRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/logs", logsView(r))
	}
}

// maxLogPage caps the rows one Logs page shows (the old limit= parameter
// still picks the page size up to this).
const maxLogPage = 1000

// logRow is one line of the Logs table: a single event, or a run of
// consecutive output lines from one command folded into one expandable row.
type logRow struct {
	Event model.LogEvent
	// Output holds the folded lines, oldest first; empty for a normal row.
	Output []model.LogEvent
	// Details are the expanded view's key/value pairs.
	Details []logDetail
	// Raw is the stored row as indented JSON (behind the "Raw" toggle).
	Raw string
}

// logDetail is one key/value pair of an expanded row. HTML values come from
// template helpers that escape their own input.
type logDetail struct {
	Key   string
	Value template.HTML
	Pre   bool // multi-line text (stderr tail)
}

func logsView(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v := req.URL.Query()
		loc := displayLoc(r)
		lq := model.ParseLogQuery(req.Context(), v, r.Inventory, loc)
		s := lq.Search
		s.ByID = true
		if s.Limit <= 0 || s.Limit > maxLogPage {
			s.Limit = model.DefaultLogPageSize
		}
		// Group filter: restrict to events whose actor is one of the group's
		// member machines (resolved to their SMBIOS UUIDs). An empty group
		// forces an empty result rather than silently showing the whole log.
		var groupID int64
		if gs := strings.TrimSpace(v.Get("group")); gs != "" {
			if gid, perr := strconv.ParseInt(gs, 10, 64); perr == nil && gid > 0 {
				groupID = gid
				if uuids, uerr := r.Groups.MemberUUIDs(req.Context(), model.ID(gid)); uerr == nil {
					if len(uuids) == 0 {
						s.Actors = []string{"\x00no-member"}
					} else {
						s.Actors = uuids
					}
				}
			}
		}
		// "Newer" pages walk up from a cursor: read oldest-first, then flip.
		var ev []model.LogEvent
		var err error
		if s.AfterID > 0 && s.BeforeID == 0 {
			ev, err = r.Logs.SearchOldestFirst(req.Context(), s)
		} else {
			ev, err = r.Logs.Search(req.Context(), s)
		}
		if err != nil {
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		count := s
		count.BeforeID, count.AfterID = 0, 0
		total, _ := r.Logs.Count(req.Context(), count)
		start := 0
		if len(ev) > 0 {
			start, _ = r.Logs.CountNewer(req.Context(), count, ev[0].ID)
		}
		end := start + len(ev) - 1

		// The Raw view shows the row as stored, before read-time defaults.
		raws := make(map[model.ID]string, len(ev))
		for _, e := range ev {
			raws[e.ID] = rawLogJSON(e)
		}
		names := namesFor(req, r)
		names.DescribeLogs(ev)
		rows := buildLogRows(ev, names, loc, raws)

		base := lq.Values()
		if groupID > 0 {
			base.Set("group", strconv.FormatInt(groupID, 10))
		}
		page := func(k string, id model.ID) string {
			u := cloneValues(base)
			if k != "" {
				u.Set(k, strconv.FormatInt(int64(id), 10))
			}
			return "/portal/logs?" + u.Encode()
		}
		data := map[string]any{
			"Rows":       rows,
			"Query":      lq,
			"Search":     s,
			"Total":      total,
			"Start":      start,
			"End":        end,
			"Groups":     nil,
			"GroupID":    model.ID(groupID),
			"Categories": model.LogCategories,
			"Levels":     model.LogLevelChoices,
			"Zone":       loc.String(),
			"Presets":    logPresets(base, loc),
			"ClearURL":   "/portal/logs",
			// Old-style filters with no field of their own on the form are
			// kept as removable chips so old bookmarks stay meaningful.
			"Legacy": legacyLogChips(base),
		}
		if len(ev) > 0 {
			if start > 1 {
				data["NewerURL"] = page("after", ev[0].ID)
				data["NewestURL"] = page("", 0)
			}
			if end < total {
				data["OlderURL"] = page("before", ev[len(ev)-1].ID)
			}
			tail := cloneValues(base)
			tail.Set("limit", "50")
			data["TailURL"] = "/api/v1/logs?" + tail.Encode()
			data["NewestID"] = int64(ev[0].ID)
			if start > 1 {
				data["NewestID"] = int64(0) // a later page: the tail starts from the top
			}
		} else {
			tail := cloneValues(base)
			tail.Set("limit", "50")
			data["TailURL"] = "/api/v1/logs?" + tail.Encode()
		}
		if groups, err := r.Groups.List(req.Context()); err == nil {
			data["Groups"] = groups
		}
		if users, err := r.Logs.Users(req.Context()); err == nil {
			data["Users"] = users
		}
		if idx, err := r.Inventory.NameIndex(req.Context()); err == nil {
			data["MachineNames"] = machineNameOptions(idx)
		}
		if lq.MachineRec != nil {
			if n, ok := names.Machine(lq.MachineRec.ID); ok {
				data["MachineLabel"] = n.Name
			}
		}
		render(w, req, r, "logs.html", "Logs", data)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// logPreset is one "Last 1h" quick range link.
type logPreset struct {
	Label, URL string
}

// logPresets builds the quick-range links: the current search with From set
// to now minus the range (in the display zone) and To cleared.
func logPresets(base url.Values, loc *time.Location) []logPreset {
	now := time.Now().In(loc)
	var out []logPreset
	for _, p := range []struct {
		label string
		d     time.Duration
	}{{"15m", 15 * time.Minute}, {"1h", time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
		u := cloneValues(base)
		u.Del("since")
		u.Del("until")
		u.Del("to")
		u.Set("from", now.Add(-p.d).Format(model.LogQueryLayout))
		out = append(out, logPreset{Label: p.label, URL: "/portal/logs?" + u.Encode()})
	}
	return out
}

// legacyChip is an old query parameter shown as a removable filter chip.
type legacyChip struct {
	Label, Value, Name, RemoveURL string
}

func legacyLogChips(base url.Values) []legacyChip {
	var out []legacyChip
	for _, k := range []struct{ name, label string }{
		{"action", "Action"}, {"actor", "Actor"}, {"component", "Component"},
		{"since", "Since"}, {"until", "Until"}, {"limit", "Per page"},
	} {
		val := base.Get(k.name)
		if val == "" {
			continue
		}
		u := cloneValues(base)
		u.Del(k.name)
		out = append(out, legacyChip{Label: k.label, Value: val, Name: k.name, RemoveURL: "/portal/logs?" + u.Encode()})
	}
	return out
}

// machineNameOptions is the machine picker's suggestion list (names only,
// sorted, capped so a huge fleet doesn't bloat the page).
func machineNameOptions(idx model.MachineNameIndex) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range idx.ByID {
		if n.Unnamed || seen[n.Name] {
			continue
		}
		seen[n.Name] = true
		out = append(out, n.Name)
	}
	sort.Strings(out)
	if len(out) > 2000 {
		out = out[:2000]
	}
	return out
}

// buildLogRows folds runs of output lines (steps.exec.stdout/stderr,
// imaging.exec.*) from the same machine into one "Output (N lines)" row and
// prepares each row's expanded details.
func buildLogRows(ev []model.LogEvent, names *model.NameLookup, loc *time.Location, raws map[model.ID]string) []logRow {
	var rows []logRow
	for i := 0; i < len(ev); i++ {
		e := ev[i]
		if model.LogOutputAction(e.Action) {
			j := i
			for j+1 < len(ev) && model.LogOutputAction(ev[j+1].Action) &&
				outputFamily(ev[j+1].Action) == outputFamily(e.Action) &&
				sameSource(ev[j+1], e) {
				j++
			}
			if j > i {
				lines := make([]model.LogEvent, 0, j-i+1)
				for k := j; k >= i; k-- { // newest-first in, oldest-first out
					lines = append(lines, ev[k])
				}
				head := ev[i]
				head.Message = fmt.Sprintf("Output (%d lines)", len(lines))
				rows = append(rows, logRow{Event: head, Output: lines})
				i = j
				continue
			}
		}
		rows = append(rows, logRow{Event: e, Details: logDetails(e, names, loc), Raw: raws[e.ID]})
	}
	return rows
}

func outputFamily(action string) string {
	if i := strings.Index(action, ".exec"); i > 0 {
		return action[:i]
	}
	return action
}

func sameSource(a, b model.LogEvent) bool {
	if a.MachineID != 0 || b.MachineID != 0 {
		return a.MachineID == b.MachineID && a.DeploymentID == b.DeploymentID
	}
	return a.Actor == b.Actor
}

// detailKeyOrder puts the most useful fields first in the expanded view.
var detailKeyOrder = []string{"error", "error_code", "exit_code", "exit_meaning", "step", "steps", "step_type", "reason", "note", "status"}

// detailSkip are fields already shown elsewhere on the row.
var detailSkip = map[string]bool{"message": true, "deployment_id": true}

func logDetails(e model.LogEvent, names *model.NameLookup, loc *time.Location) []logDetail {
	esc := func(s string) template.HTML { return template.HTML(template.HTMLEscapeString(s)) }
	var out []logDetail
	add := func(k string, v template.HTML) {
		if v != "" {
			out = append(out, logDetail{Key: k, Value: v})
		}
	}
	add("When", esc(absTime(e.OccurredAt, loc)))
	if e.MachineID > 0 {
		add("Machine", entityLinkHTML(names, model.KindMachine, e.MachineID))
	}
	if e.DeploymentID > 0 {
		add("Deployment", template.HTML(fmt.Sprintf(`<a href="/portal/logs?deployment=%d">Run #%d</a> · all events of this run`, int64(e.DeploymentID), int64(e.DeploymentID))))
	}
	if e.User != "" {
		add("User", esc(e.User))
	}
	if e.Target != "" {
		if e.TargetKind != "" && e.TargetID > 0 {
			add("Target", entityLinkHTML(names, e.TargetKind, e.TargetID))
		} else {
			add("Target", esc(e.Target))
		}
	}
	if e.Outcome != "" {
		add("Outcome", esc(e.Outcome))
	}
	add("Source", esc(strings.TrimSpace(e.Component+" · "+e.Action)))
	if e.Actor != "" && e.Actor != e.User {
		add("Actor", esc(e.Actor))
	}
	f := model.ParseLogFields(e.Fields)
	keys := make([]string, 0, len(f))
	for k := range f {
		if !detailSkip[k] {
			keys = append(keys, k)
		}
	}
	rank := func(k string) int {
		for i, o := range detailKeyOrder {
			if o == k {
				return i
			}
		}
		return len(detailKeyOrder)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := rank(keys[i]), rank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		val := model.LogFieldText(f[k])
		if val == "" {
			continue
		}
		d := logDetail{Key: humanKey(k), Value: esc(val)}
		if id, ok := fieldEntity(k, f[k]); ok {
			d.Value = entityLinkHTML(names, id.kind, id.id)
		}
		if strings.Contains(val, "\n") || strings.HasSuffix(k, "_tail") {
			d.Pre = true
		}
		out = append(out, d)
	}
	return out
}

type fieldRef struct {
	kind string
	id   model.ID
}

// fieldEntity links id-valued fields (image_id, machine_id, …) to the
// entity they name.
func fieldEntity(key string, v any) (fieldRef, bool) {
	if !strings.HasSuffix(key, "_id") {
		return fieldRef{}, false
	}
	kind := model.NormalizeKind(strings.TrimSuffix(key, "_id"))
	if kind == "" {
		return fieldRef{}, false
	}
	var n int64
	switch x := v.(type) {
	case float64:
		n = int64(x)
	case string:
		n, _ = strconv.ParseInt(x, 10, 64)
	}
	if n <= 0 {
		return fieldRef{}, false
	}
	return fieldRef{kind: kind, id: model.ID(n)}, true
}

// humanKey turns "exit_code" into "Exit code".
func humanKey(k string) string {
	s := strings.ReplaceAll(k, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// rawLogJSON is the stored row as indented JSON, with the fields object
// inlined rather than as an escaped string. Call it before DescribeLogs.
func rawLogJSON(e model.LogEvent) string {
	row := map[string]any{
		"id": e.ID, "occurred_at": e.OccurredAt, "component": e.Component,
		"level": e.Level, "actor": e.Actor, "action": e.Action, "target": e.Target,
	}
	for k, v := range map[string]string{"message": e.Message, "user": e.User, "outcome": e.Outcome, "error": e.Error, "hint": e.Hint} {
		if v != "" {
			row[k] = v
		}
	}
	if e.MachineID > 0 {
		row["machine_id"] = e.MachineID
	}
	if e.DeploymentID > 0 {
		row["deployment_id"] = e.DeploymentID
	}
	var f any
	if json.Unmarshal([]byte(e.Fields), &f) == nil {
		row["fields"] = f
	} else {
		row["fields"] = e.Fields
	}
	b, _ := json.MarshalIndent(row, "", "  ")
	return string(b)
}

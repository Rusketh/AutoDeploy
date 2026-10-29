package portal

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Plain-language views of bulk operations: the status label an operator
// reads (derived from the stored lifecycle status AND the job rollup, so a
// "completed" run with failures doesn't read as a success), the segmented
// progress bar, and schedules in words. Shared by the bulk list, detail and
// review pages, and the Windows Update deployment page.

// statusView is a status badge: Tone is the badge class (ok, bad, warn,
// muted, accent or "" for neutral), Icon an #i-… symbol, Note an optional
// short line of context ("2 failed so far").
type statusView struct {
	Label string
	Tone  string
	Icon  string
	Note  string
}

// bulkStatus derives the label an operator sees for an operation. The
// stored status says where the lifecycle is; the rollup says how it went.
func bulkStatus(op model.BulkOperation) statusView {
	p := op.Progress
	switch op.Status {
	case model.BulkStatusCancelled:
		v := statusView{Label: "Cancelled", Tone: "muted", Icon: "i-x"}
		if p.OK > 0 || p.Failed > 0 {
			v.Note = fmt.Sprintf("%d of %d finished before it was stopped", p.OK+p.Failed, p.Total)
		}
		return v
	case model.BulkStatusPaused:
		return statusView{Label: "Paused", Tone: "muted", Icon: "i-pause", Note: "Recurring runs are on hold"}
	case model.BulkStatusScheduled:
		return statusView{Label: "Scheduled", Tone: "", Icon: "i-clock"}
	}
	outstanding := p.Queued + p.Running
	if op.Status == model.BulkStatusActive && outstanding > 0 {
		v := statusView{Label: "Running", Tone: "accent", Icon: "i-play"}
		if p.Failed > 0 {
			v.Note = fmt.Sprintf("%d failed so far", p.Failed)
		}
		return v
	}
	if op.Status == model.BulkStatusActive && op.ScheduleKind == model.BulkScheduleRecurring {
		// Between runs: say it repeats, and how the last run went.
		v := statusView{Label: "Recurring", Tone: "accent", Icon: "i-repeat"}
		if p.Total > 0 {
			v.Note = "Last run: " + strings.ToLower(finishedLabel(p))
		}
		return v
	}
	// Completed (or an active run whose last job just finished and is
	// about to flip to completed): judge it by its jobs.
	v := statusView{Label: finishedLabel(p), Tone: "ok", Icon: "i-check"}
	switch {
	case p.Total == 0:
		v.Tone, v.Icon, v.Note = "muted", "i-info", "No machines matched the target"
	case p.Failed > 0 && p.OK == 0:
		v.Tone, v.Icon = "bad", "i-alert"
	case p.Failed > 0:
		v.Tone, v.Icon = "warn", "i-alert"
		if p.Failed*2 >= p.Total {
			v.Tone = "bad"
		}
	case p.Cancelled > 0:
		v.Tone, v.Icon = "warn", "i-alert"
		v.Note = "Some machines didn't check in before the give-up time"
	}
	return v
}

// finishedLabel is the outcome of a finished run in words.
func finishedLabel(p model.BulkProgress) string {
	switch {
	case p.Total == 0:
		return "Completed"
	case p.Failed > 0 && p.OK == 0:
		return fmt.Sprintf("Failed on all %d machine%s", p.Total, plural(p.Total))
	case p.Failed > 0:
		return fmt.Sprintf("Completed with %d failure%s", p.Failed, plural(p.Failed))
	case p.Cancelled > 0:
		return fmt.Sprintf("Completed · %d not delivered", p.Cancelled)
	}
	return "Completed"
}

// progressSeg is one coloured run of a segmented progress bar.
type progressSeg struct {
	Key   string // ok | failed | running | queued | cancelled (CSS + JS hook)
	Label string // legend text
	Count int
	Pct   float64 // width, 0-100
}

// segmentedProgress drives the "seg_progress" partial: the bar, its legend
// and a sentence for screen readers.
type segmentedProgress struct {
	Total   int
	Segs    []progressSeg
	Summary string // "15 machines: 12 succeeded, 3 failed"
	Short   string // "12 succeeded · 3 failed" (compact rows)
	// Compact drops the legend (list rows show a one-line summary).
	Compact bool
}

// newSegmentedProgress lays out counts in a fixed order. Every key is
// present (zero-width when empty) so the live poller can update widths.
func newSegmentedProgress(total int, segs ...progressSeg) segmentedProgress {
	sp := segmentedProgress{Total: total}
	var parts []string
	for _, s := range segs {
		if total > 0 {
			s.Pct = float64(s.Count) * 100 / float64(total)
		}
		sp.Segs = append(sp.Segs, s)
		if s.Count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", s.Count, strings.ToLower(s.Label)))
		}
	}
	if total == 0 {
		sp.Summary, sp.Short = "No jobs yet", "No jobs yet"
	} else {
		sp.Summary = fmt.Sprintf("%d machine%s: %s", total, plural(total), strings.Join(parts, ", "))
		sp.Short = strings.Join(parts, " · ")
	}
	return sp
}

// bulkProgressBar is the segmented bar for a bulk rollup.
func bulkProgressBar(p model.BulkProgress) segmentedProgress {
	return newSegmentedProgress(p.Total,
		progressSeg{Key: "ok", Label: "Succeeded", Count: p.OK},
		progressSeg{Key: "failed", Label: "Failed", Count: p.Failed},
		progressSeg{Key: "running", Label: "Running", Count: p.Running},
		progressSeg{Key: "queued", Label: "Queued", Count: p.Queued},
		progressSeg{Key: "cancelled", Label: "Cancelled", Count: p.Cancelled},
	)
}

// jobStatusView labels one job row (bulk and Windows Update jobs share the
// same status tokens).
func jobStatusView(status string) statusView {
	switch status {
	case "ok":
		return statusView{Label: "Succeeded", Tone: "ok", Icon: "i-check"}
	case "failed":
		return statusView{Label: "Failed", Tone: "bad", Icon: "i-alert"}
	case "running":
		return statusView{Label: "Running", Tone: "accent", Icon: "i-play"}
	case "queued":
		return statusView{Label: "Queued", Tone: "", Icon: "i-clock"}
	case "cancelled":
		return statusView{Label: "Cancelled", Tone: "muted", Icon: "i-x"}
	}
	return statusView{Label: humanToken(status), Tone: "muted", Icon: "i-info"}
}

// humanToken turns a raw token ("not_applicable") into words.
func humanToken(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "_", " "), "-", " "))
	if s == "" {
		return "Unknown"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// jobResultText reduces an agent's result JSON to the sentence worth
// showing: its error or message, else its exit code. Anything it can't
// read comes back as-is.
func jobResultText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return raw
	}
	for _, k := range []string{"error", "message", "detail"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	if v, ok := m["exit_code"]; ok {
		return fmt.Sprintf("Exit code %v", v)
	}
	return raw
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// recurText describes a stored RecurSpec in words — "every day at 02:00",
// "every 2 weeks on Monday at 06:30" — with the zone its wall-clock times
// are in. A cron spec is shown as-is.
func recurText(spec string, loc *time.Location) string {
	var rs model.RecurSpec
	if json.Unmarshal([]byte(spec), &rs) != nil {
		return "on a custom schedule"
	}
	return recurSpecText(rs, loc)
}

func recurSpecText(rs model.RecurSpec, loc *time.Location) string {
	zone := ""
	if loc != nil {
		zone = " (" + loc.String() + ")"
	}
	if strings.TrimSpace(rs.Cron) != "" {
		return "on a custom schedule: cron “" + strings.TrimSpace(rs.Cron) + "”" + zone
	}
	every := rs.Every
	if every < 1 {
		every = 1
	}
	at := rs.At
	if at == "" {
		at = "00:00"
	}
	switch rs.Unit {
	case "hour":
		if every == 1 {
			return "every hour"
		}
		return fmt.Sprintf("every %d hours", every)
	case "day":
		if every == 1 {
			return "every day at " + at + zone
		}
		return fmt.Sprintf("every %d days at %s%s", every, at, zone)
	case "week":
		day := "Sunday"
		if rs.Weekday >= 0 && rs.Weekday < len(weekdays) {
			day = weekdays[rs.Weekday]
		}
		if every == 1 {
			return "every " + day + " at " + at + zone
		}
		return fmt.Sprintf("every %d weeks on %s at %s%s", every, day, at, zone)
	}
	return "on a repeating schedule"
}

// scheduleText is an operation's schedule in words, for lists and detail:
// "Run now", "Once, Sat 31 Oct 2026 02:00 GMT", "Every day at 02:00 (UTC)".
func scheduleText(op model.BulkOperation, loc *time.Location) string {
	switch op.ScheduleKind {
	case model.BulkScheduleOnce:
		if op.RunAt != nil {
			return "Once, " + op.RunAt.In(loc).Format("Mon 2 Jan 2006 15:04 MST")
		}
		return "Once"
	case model.BulkScheduleRecurring:
		t := recurText(op.RecurSpec, loc)
		return strings.ToUpper(t[:1]) + t[1:]
	}
	return "Run now"
}

// bulkActionDetail is an operation's payload decoded for display.
type bulkActionDetail struct {
	Find, Replace     string // rename
	Shell, ShellLabel string // script
	Body              string
	PackageID         int64 // software_push
}

// decodeBulkPayload reads the action-specific payload fields. A payload it
// can't read yields an empty detail (the raw JSON stays on the page).
func decodeBulkPayload(action, payload string) bulkActionDetail {
	var d bulkActionDetail
	switch action {
	case model.BulkActionRename:
		var raw map[string]string
		_ = json.Unmarshal([]byte(payload), &raw)
		d.Find, d.Replace = raw["rename_find"], raw["rename_replace"]
	case model.BulkActionScript:
		var raw map[string]string
		_ = json.Unmarshal([]byte(payload), &raw)
		d.Shell, d.Body = raw["shell"], raw["body"]
		d.ShellLabel = shellLabel(d.Shell)
	case model.BulkActionSoftwarePush:
		var raw map[string]int64
		_ = json.Unmarshal([]byte(payload), &raw)
		d.PackageID = raw["package_id"]
	}
	return d
}

func shellLabel(shell string) string {
	switch shell {
	case "cmd":
		return "Command Prompt (cmd) script"
	case "", "powershell":
		return "PowerShell script"
	}
	return shell + " script"
}

// namesShown is how many machine names a summary lists before "and N more".
const namesShown = 20

// joinNamesHTML lists up to namesShown already-escaped name fragments,
// then "and N more" for the rest of total.
func joinNamesHTML(parts []template.HTML, total int) template.HTML {
	var b strings.Builder
	for i, p := range parts {
		if i == namesShown {
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(p))
	}
	if total > namesShown {
		fmt.Fprintf(&b, ` <span class="muted">and %d more</span>`, total-namesShown)
	}
	return template.HTML(b.String())
}

// bulkTargetHTML describes what an operation acts on in names: the
// machines of a fixed selection (linked, first 20), or the filter a
// re-evaluated target runs — machine group by name, name pattern, OU, AD
// group.
func bulkTargetHTML(names *model.NameLookup, t model.BulkTarget, mode string) template.HTML {
	esc := template.HTMLEscapeString
	if n := len(t.MachineIDs); n > 0 && mode != model.BulkTargetFilter {
		parts := make([]template.HTML, 0, min(n, namesShown))
		for i, id := range t.MachineIDs {
			if i == namesShown {
				break
			}
			parts = append(parts, entityLinkHTML(names, model.KindMachine, id))
		}
		return template.HTML(fmt.Sprintf("%d machine%s: ", n, plural(n))) + joinNamesHTML(parts, n)
	}
	var crit []string
	if t.GroupID != 0 {
		crit = append(crit, "in machine group "+string(entityLinkHTML(names, model.KindGroup, t.GroupID)))
	}
	if t.NameRegex != "" {
		crit = append(crit, "named like <code>"+esc(t.NameRegex)+"</code>")
	}
	if t.OU != "" {
		crit = append(crit, "in OU <code>"+esc(t.OU)+"</code>")
	}
	if t.Group != "" {
		crit = append(crit, "in AD group <code>"+esc(t.Group)+"</code>")
	}
	if len(crit) == 0 {
		return template.HTML(`<span class="muted">No machines</span>`)
	}
	out := "Every machine " + strings.Join(crit, " and ")
	if mode == model.BulkTargetFilter {
		out += ` <span class="muted">— looked up again at each run</span>`
	}
	return template.HTML(out)
}

// bulkTemplateFuncs are the bulk/status helpers every page can use.
func bulkTemplateFuncs(loc *time.Location) template.FuncMap {
	return template.FuncMap{
		"bulkStatus":      bulkStatus,
		"bulkProgressBar": bulkProgressBar,
		"compactProgress": func(sp segmentedProgress) segmentedProgress { sp.Compact = true; return sp },
		"jobStatus":       jobStatusView,
		"jobResult":       jobResultText,
		"actionLabel":     model.BulkActionLabel,
		"scheduleText":    func(op model.BulkOperation) string { return scheduleText(op, loc) },
		"humanToken":      humanToken,
		// Windows Update pages (windowsupdate.go).
		"osLabel":          osFilterLabel,
		"severityTone":     severityTone,
		"complianceStatus": complianceStatusView,
		"compliance":       complianceView,
		"complianceBar":    complianceBar,
		"deploymentBar":    deploymentBar,
		"deploymentStatus": deploymentStatus,
	}
}

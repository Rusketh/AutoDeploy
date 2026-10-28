package model

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// DescribeLogs prepares events for display: EnrichLogs (machine and target
// names), then the read-time defaults for columns older rows don't have —
// MachineID from the actor or a machine target, and Message / Hint /
// Outcome / Error from the action catalogue. Category and the effective
// Level (catalogue overrides applied) are set on every row.
func (l *NameLookup) DescribeLogs(events []LogEvent) {
	l.EnrichLogs(events)
	for i := range events {
		ev := &events[i]
		if ev.MachineID == 0 {
			if id, _, ok := l.MachineByActor(ev.Actor); ok {
				ev.MachineID = id
			} else if ev.TargetKind == KindMachine && ev.TargetID > 0 {
				ev.MachineID = ev.TargetID
			}
		}
		if ev.MachineID > 0 && ev.MachineName == "" {
			if n, ok := l.Machine(ev.MachineID); ok {
				ev.MachineName = n.Name
			}
		}
		ApplyLogDescription(ev, LogNames{Machine: ev.MachineName, Target: ev.TargetName})
	}
}

// ApplyLogDescription fills an event's catalogue-derived columns that are
// still empty, and sets Category and the effective Level.
func ApplyLogDescription(ev *LogEvent, names LogNames) {
	d := DescribeLog(*ev, names)
	if ev.Message == "" {
		ev.Message = d.Message
	}
	if ev.Hint == "" {
		ev.Hint = d.Hint
	}
	if ev.Outcome == "" {
		ev.Outcome = d.Outcome
	}
	if ev.Error == "" {
		ev.Error = d.Error
	}
	ev.Category = d.Category
	ev.Level = d.Level
}

// PrepareIngestedLog derives the 0035 columns for an event a client just
// shipped: deployment_id and error from its fields, then the catalogue
// message / hint / outcome. Target names are resolved through l for every
// kind except machines (loading the whole machine index per ingest batch
// would make ingest expensive; machine names are resolved at read time).
// The stored level is left as the client sent it — read-time overrides
// handle the few actions older clients mislevelled.
func PrepareIngestedLog(ev *LogEvent, l *NameLookup) {
	f := parseLogFields(ev.Fields)
	if ev.DeploymentID == 0 {
		if n, ok := fieldInt(f, "deployment_id"); ok && n > 0 {
			ev.DeploymentID = ID(n)
		}
	}
	var names LogNames
	machineTarget := false
	if kind, id, _, ok := ParseLogTarget(ev.Target); ok {
		if kind == KindMachine {
			machineTarget = true
		} else if id > 0 && l != nil {
			if res := l.Lookup(kind, id); res.Loaded {
				names.Target = res.Label(id)
			}
		}
	}
	level := ev.Level
	ApplyLogDescription(ev, names)
	ev.Level = level
	ev.Category = ""
	// Only keep a message that is final: an uncatalogued action's humanised
	// code, or one naming a machine target we didn't resolve here, is left
	// NULL so the read path renders it with names (and with any catalogue
	// entry a later release adds).
	if (!HasLogCatalogEntry(ev.Action) && fieldString(parseLogFields(ev.Fields), "message") == "") ||
		(machineTarget && strings.Contains(ev.Message, ev.Target)) {
		ev.Message = ""
	}
}

// Record appends a server-side event (an audit entry, a deployment
// milestone) with its message, hint and outcome rendered now, so the row
// keeps the names things had when it happened. names resolves the target
// (any kind, machines included); it may be nil.
func (r *LogRepo) Record(ctx context.Context, ev LogEvent, names *NameLookup) error {
	if r == nil {
		return nil
	}
	var ln LogNames
	if kind, id, ref, ok := ParseLogTarget(ev.Target); ok && names != nil {
		switch {
		case kind == KindMachine && id == 0:
			if _, n, found := names.MachineByActor(ref); found {
				ln.Target = n.Name
			}
		default:
			if res := names.Lookup(kind, id); res.Loaded {
				ln.Target = res.Label(id)
			}
		}
		if kind == KindMachine && ev.MachineID == 0 {
			ev.MachineID = id
		}
	}
	if ev.MachineID > 0 && names != nil {
		if n, ok := names.Machine(ev.MachineID); ok {
			ln.Machine = n.Name
		}
	}
	level := ev.Level
	ApplyLogDescription(&ev, ln)
	if level != "" {
		ev.Level = level
	}
	ev.Category = ""
	return r.Append(ctx, ev)
}

// GetDeployment returns one deployment_history row.
func (r *InventoryRepo) GetDeployment(ctx context.Context, id ID) (DeploymentRecord, error) {
	var d DeploymentRecord
	var imageID sql.NullInt64
	var completed sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, machine_id, image_id, started_at, completed_at, outcome, notes, phase, progress_done, progress_total
		FROM deployment_history WHERE id=?`, id).Scan(&d.ID, &d.MachineID, &imageID, &d.StartedAt,
		&completed, &d.Outcome, &d.Notes, &d.Phase, &d.ProgressDone, &d.ProgressTotal)
	if err == sql.ErrNoRows {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.ImageID = idPtr(imageID)
	if completed.Valid {
		t := completed.Time
		d.CompletedAt = &t
	}
	return d, nil
}

// DeployTimeline is the condensed story of one deployment run: its key
// events oldest first (boot stages → setup phases → package results) and
// the first error, with its hint, as the headline.
type DeployTimeline struct {
	Events []LogEvent
	// Headline is the first ERROR of the run (nil when none).
	Headline *LogEvent
	// Total is how many log events the run has in all.
	Total int
	// Hidden is how many key events were cut to keep the timeline short.
	Hidden int
}

// maxTimelineEvents caps the key events a timeline shows.
const maxTimelineEvents = 40

// timelineActions are the milestones a deployment timeline keeps (every
// WARN/ERROR event is kept too).
var timelineActions = map[string]bool{
	"deploy.started": true, "deploy.reimage": true, "boot.start": true,
	"manifest.received": true, "deploy.disk.resolved": true, "deploy.disk.auto": true,
	"deploy.stage.start": true, "download.media.ok": true, "deploy.stage.ok": true,
	"deploy.staged": true, "deploy.setup": true, "agent.start": true,
	"domainjoin.ok": true, "rename.desired": true, "package.install.ok": true,
	"package.skip": true, "update.install.ok": true, "agent.done": true,
	"deploy.finished": true, "deploy.failed": true, "deploy.fail.choice": true,
}

// maxTimelineScan bounds how many of a run's rows a timeline reads.
const maxTimelineScan = 2000

// DeploymentTimeline builds a run's timeline. m is the run's machine (its
// identifiers let rows from older, untagged clients join the run by time);
// names resolves machine and target names (may be nil).
func (r *LogRepo) DeploymentTimeline(ctx context.Context, d DeploymentRecord, m MachineRecord, names *NameLookup) (DeployTimeline, error) {
	end := time.Now()
	if d.CompletedAt != nil {
		end = *d.CompletedAt
	}
	s := LogSearch{
		DeploymentID: d.ID, ByID: true, Limit: maxTimelineScan,
		Window: &LogWindow{From: d.StartedAt.Add(-10 * time.Minute), To: end.Add(15 * time.Minute), Machine: LogMachineFor(m)},
	}
	evs, err := r.Search(ctx, s)
	if err != nil {
		return DeployTimeline{}, err
	}
	names.DescribeLogs(evs)
	tl := DeployTimeline{Total: len(evs)}
	for i := len(evs) - 1; i >= 0; i-- { // oldest first
		ev := evs[i]
		if LogOutputAction(ev.Action) {
			continue
		}
		if ev.Level == "ERROR" && tl.Headline == nil && ev.Action != "deploy.failed" {
			e := ev
			tl.Headline = &e
		}
		if timelineActions[ev.Action] || ev.Level == "ERROR" || ev.Level == "WARN" {
			tl.Events = append(tl.Events, ev)
		}
	}
	if tl.Headline == nil {
		for _, ev := range tl.Events {
			if ev.Action == "deploy.failed" {
				e := ev
				tl.Headline = &e
				break
			}
		}
	}
	if len(tl.Events) > maxTimelineEvents {
		tl.Hidden = len(tl.Events) - maxTimelineEvents
		tl.Events = tl.Events[:maxTimelineEvents]
	}
	return tl, nil
}

// LogMachineFor builds the LogSearch machine filter for a machine row.
func LogMachineFor(m MachineRecord) LogMachine {
	lm := LogMachine{ID: m.ID}
	for _, a := range []string{m.SystemUUID, m.AgentID} {
		if a = strings.TrimSpace(a); a != "" {
			lm.Actors = append(lm.Actors, a)
		}
	}
	return lm
}

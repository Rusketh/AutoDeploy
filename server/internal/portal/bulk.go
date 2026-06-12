package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

func init() {
	registerBulkRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/bulk", bulkList(r))
		get("/portal/bulk/new", bulkFormNew(r))
		get("/portal/bulk/search", bulkSearch(r))
		post("/portal/bulk", bulkCreate(r))
		post("/portal/bulk/clear-completed", bulkClearCompleted(r))
		get("/portal/bulk/{id}", bulkDetail(r))
		get("/portal/bulk/{id}/edit", bulkEditForm(r))
		post("/portal/bulk/{id}/edit", bulkEditSave(r))
		get("/portal/bulk/{id}/progress", bulkProgress(r))
		post("/portal/bulk/{id}/cancel", bulkCancel(r))
		post("/portal/bulk/{id}/pause", bulkPause(r))
		post("/portal/bulk/{id}/resume", bulkResume(r))
		post("/portal/bulk/{id}/run-now", bulkRunNow(r))
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func bulkList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ops, err := r.Bulk.ListOperationsWithProgress(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "bulk_list.html", "Bulk operations", map[string]any{"Ops": ops})
	}
}

func bulkFormNew(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		pkgs, _ := r.Software.List(req.Context())
		imgs, _ := r.Images.List(req.Context())
		render(w, req, r, "bulk_form.html", "New bulk operation", map[string]any{
			"Packages":    pkgs,
			"Images":      imgs,
			"FormAction":  "/portal/bulk",
			"Editing":     false,
			"AP":          formPrefill(nil),
			"SelInitJSON": "[]",
		})
	}
}

// bulkSearch returns machines matching the filter as JSON, for the
// "build a selection" UI. The filter is a SEARCH TOOL -- the operator adds
// individual results to a selection basket; the basket (machine_ids) is
// what the action actually runs against, not the filter itself.
func bulkSearch(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		t := model.BulkTarget{
			NameRegex: strings.TrimSpace(req.URL.Query().Get("name_regex")),
			OU:        strings.TrimSpace(req.URL.Query().Get("ou")),
			Group:     strings.TrimSpace(req.URL.Query().Get("group")),
		}
		machines, err := r.Bulk.PreviewTargets(req.Context(), t)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSONResp(w, machineItems(r, req, machines))
	}
}

type machineItem struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	UUID    string `json:"uuid"`
	Make    string `json:"make"`
	Product string `json:"product"`
}

func machineItems(r Repos, req *http.Request, machines []model.MachineRecord) []machineItem {
	out := make([]machineItem, 0, len(machines))
	for _, m := range machines {
		b, _ := r.Inventory.GetBinding(req.Context(), m.ID)
		out = append(out, machineItem{
			ID: int64(m.ID), Name: b.MachineName, UUID: m.SystemUUID,
			Make: m.SystemManufacturer, Product: m.SystemProduct,
		})
	}
	return out
}

// bulkParams collects the action payload, target and schedule from a create or
// edit form. flashErr is set (and the caller redirects) when validation fails.
type bulkParams struct {
	Name           string
	Description    string
	Action         string
	Payload        string
	Target         model.BulkTarget
	TargetMode     string
	ReimageImageID model.ID
	ScheduleKind   string
	RunAt          *time.Time
	RecurSpec      string
	// Delivery options.
	WakeOnLAN          bool
	CancelAfterMinutes int
}

// parseBulkForm reads the shared action + target + schedule fields. It returns
// a human-readable error suitable for a flash on failure.
func parseBulkForm(req *http.Request) (bulkParams, error) {
	if err := req.ParseForm(); err != nil {
		return bulkParams{}, err
	}
	var p bulkParams
	// Optional label + notes. Blank is fine -- the model fills a sensible
	// default from the action and target.
	p.Name = strings.TrimSpace(req.FormValue("name"))
	p.Description = strings.TrimSpace(req.FormValue("description"))
	p.Action = req.FormValue("action")

	// Schedule.
	p.ScheduleKind = req.FormValue("schedule_kind")
	if p.ScheduleKind == "" {
		p.ScheduleKind = model.BulkScheduleNow
	}
	switch p.ScheduleKind {
	case model.BulkScheduleNow:
	case model.BulkScheduleOnce:
		t, err := parseLocalDateTime(req.FormValue("run_at"))
		if err != nil {
			return p, fmt.Errorf("choose when the one-time operation should run")
		}
		p.RunAt = &t
	case model.BulkScheduleRecurring:
		spec, err := recurSpecFromForm(req)
		if err != nil {
			return p, err
		}
		p.RecurSpec = spec
	default:
		return p, fmt.Errorf("unknown schedule type")
	}

	// Delivery options: Wake-on-LAN and the cancel-after window.
	p.WakeOnLAN = req.FormValue("wake_on_lan") != ""
	cancelAfter, err := cancelAfterFromForm(req)
	if err != nil {
		return p, err
	}
	p.CancelAfterMinutes = cancelAfter

	// Target. A recurring "filter" task stores the live filter so each run
	// re-resolves it; everything else uses the explicit selection basket.
	p.TargetMode = req.FormValue("target_mode")
	if p.ScheduleKind == model.BulkScheduleRecurring && p.TargetMode == model.BulkTargetFilter {
		p.Target = model.BulkTarget{
			NameRegex: strings.TrimSpace(req.FormValue("filter_name")),
			OU:        strings.TrimSpace(req.FormValue("filter_ou")),
			Group:     strings.TrimSpace(req.FormValue("filter_group")),
		}
		if p.Target.NameRegex == "" && p.Target.OU == "" && p.Target.Group == "" {
			return p, fmt.Errorf("a re-evaluated recurring task needs a name, OU or group filter")
		}
	} else {
		p.TargetMode = model.BulkTargetSelection
		var ids []model.ID
		for _, s := range req.Form["machine_ids"] {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
				ids = append(ids, model.ID(n))
			}
		}
		if len(ids) == 0 {
			return p, fmt.Errorf("add at least one machine to the selection")
		}
		p.Target = model.BulkTarget{MachineIDs: ids}
	}

	// Action payload (shared with single-machine actions, minus the
	// literal single-machine rename which bulk forbids).
	switch p.Action {
	case model.BulkActionRename:
		find := strings.TrimSpace(req.FormValue("rename_find"))
		if find == "" {
			return p, fmt.Errorf("bulk rename requires a find pattern. To rename one machine to a literal name, use its machine page")
		}
		b, _ := json.Marshal(map[string]string{
			"rename_find":    find,
			"rename_replace": req.FormValue("rename_replace"),
		})
		p.Payload = string(b)
	case model.BulkActionScript:
		body := req.FormValue("script_body")
		if strings.TrimSpace(body) == "" {
			return p, fmt.Errorf("script body required")
		}
		b, _ := json.Marshal(map[string]string{"shell": req.FormValue("script_shell"), "body": body})
		p.Payload = string(b)
	case model.BulkActionSoftwarePush:
		pid, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64)
		if err != nil || pid <= 0 {
			return p, fmt.Errorf("choose a software package to push")
		}
		b, _ := json.Marshal(map[string]int64{"package_id": pid})
		p.Payload = string(b)
	case model.BulkActionReimage:
		p.Payload = "{}"
		p.ReimageImageID = reimageImageIDFromForm(req)
	default:
		return p, fmt.Errorf("unknown action")
	}
	return p, nil
}

// cancelAfterFromForm reads the optional cancel-after window: a number plus
// a minutes/hours unit, normalised to minutes. Blank or 0 = never cancel.
func cancelAfterFromForm(req *http.Request) (int, error) {
	raw := strings.TrimSpace(req.FormValue("cancel_after_value"))
	if raw == "" || raw == "0" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("cancel-after must be a whole number (or blank for never)")
	}
	switch req.FormValue("cancel_after_unit") {
	case "minutes":
		return n, nil
	case "", "hours":
		return n * 60, nil
	default:
		return 0, fmt.Errorf("cancel-after unit must be minutes or hours")
	}
}

// parseLocalDateTime parses an <input type=datetime-local> value (no zone) in
// the server's local timezone.
func parseLocalDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad datetime %q", s)
}

// recurSpecFromForm builds the RecurSpec JSON from the friendly preset fields
// (or an advanced cron expression when supplied).
func recurSpecFromForm(req *http.Request) (string, error) {
	if cron := strings.TrimSpace(req.FormValue("recur_cron")); cron != "" {
		b, _ := json.Marshal(model.RecurSpec{Cron: cron})
		return string(b), nil
	}
	every, _ := strconv.Atoi(strings.TrimSpace(req.FormValue("recur_every")))
	if every < 1 {
		every = 1
	}
	unit := req.FormValue("recur_unit")
	switch unit {
	case "hour", "day", "week":
	default:
		return "", fmt.Errorf("choose how often the recurring task runs")
	}
	rs := model.RecurSpec{Every: every, Unit: unit}
	if unit == "day" || unit == "week" {
		rs.At = strings.TrimSpace(req.FormValue("recur_at"))
		if rs.At == "" {
			rs.At = "00:00"
		}
	}
	if unit == "week" {
		rs.Weekday, _ = strconv.Atoi(req.FormValue("recur_weekday"))
	}
	b, _ := json.Marshal(rs)
	return string(b), nil
}

func bulkCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p, err := parseBulkForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		user, _ := sessionUser(req, r)
		op, jobs, err := r.Bulk.CreateOperation(req.Context(), model.BulkOperation{
			Name:               p.Name,
			Description:        p.Description,
			Action:             p.Action,
			Payload:            p.Payload,
			Target:             p.Target,
			TargetMode:         p.TargetMode,
			CreatedBy:          user.Username,
			ReimageImageID:     p.ReimageImageID,
			ScheduleKind:       p.ScheduleKind,
			RunAt:              p.RunAt,
			RecurSpec:          p.RecurSpec,
			WakeOnLAN:          p.WakeOnLAN,
			CancelAfterMinutes: p.CancelAfterMinutes,
		})
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		r.Emitter.Emit(req.Context(), notify.BulkCreatedEvent(op))
		// "Run now" queued its jobs above — wake the targets; scheduled and
		// recurring runs are woken by the scheduler when they materialise.
		if op.WakeOnLAN && r.Waker != nil && len(jobs) > 0 {
			r.Waker.WakeForJobs(req.Context(), jobs)
		}
		switch p.ScheduleKind {
		case model.BulkScheduleOnce:
			flash(w, "ok", "Operation scheduled for "+op.RunAt.Local().Format("Mon 2 Jan 15:04")+".")
		case model.BulkScheduleRecurring:
			flash(w, "ok", "Recurring operation scheduled.")
		default:
			flash(w, "ok", "Operation queued.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", op.ID), http.StatusFound)
	}
}

func bulkEditForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		op, _, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		pkgs, _ := r.Software.List(req.Context())
		imgs, _ := r.Images.List(req.Context())
		// Pre-resolve the frozen selection so the basket is populated.
		var selInit []machineItem
		if len(op.Target.MachineIDs) > 0 {
			ms, _ := r.Bulk.PreviewTargets(req.Context(), model.BulkTarget{MachineIDs: op.Target.MachineIDs})
			selInit = machineItems(r, req, ms)
		}
		selJSON, _ := json.Marshal(selInit)
		render(w, req, r, "bulk_form.html", fmt.Sprintf("Edit bulk operation #%d", int64(id)), map[string]any{
			"Packages":    pkgs,
			"Images":      imgs,
			"FormAction":  fmt.Sprintf("/portal/bulk/%d/edit", int64(id)),
			"Editing":     true,
			"Op":          op,
			"AP":          formPrefill(&op),
			"SelInitJSON": string(selJSON),
		})
	}
}

func bulkEditSave(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := parseBulkForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d/edit", int64(id)), http.StatusFound)
			return
		}
		if err := r.Bulk.UpdateOperation(req.Context(), model.BulkOperation{
			ID:                 id,
			Name:               p.Name,
			Description:        p.Description,
			Action:             p.Action,
			Payload:            p.Payload,
			Target:             p.Target,
			TargetMode:         p.TargetMode,
			ReimageImageID:     p.ReimageImageID,
			ScheduleKind:       p.ScheduleKind,
			RunAt:              p.RunAt,
			RecurSpec:          p.RecurSpec,
			WakeOnLAN:          p.WakeOnLAN,
			CancelAfterMinutes: p.CancelAfterMinutes,
		}); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d/edit", int64(id)), http.StatusFound)
			return
		}
		flash(w, "ok", "Operation updated.")
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

func bulkCancel(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Bulk.CancelOperation(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Operation cancelled. Queued and running jobs were stopped.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

func bulkPause(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Bulk.PauseOperation(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Recurring operation paused.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

func bulkResume(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Bulk.ResumeOperation(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Recurring operation resumed.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

func bulkRunNow(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if jobs, err := r.Bulk.RunOperationNow(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			_ = r.Bulk.AdvanceSchedule(req.Context(), id, time.Now())
			if op, _, oerr := r.Bulk.GetOperation(req.Context(), id); oerr == nil &&
				op.WakeOnLAN && r.Waker != nil {
				r.Waker.WakeForJobs(req.Context(), jobs)
			}
			flash(w, "ok", "A run was queued now.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

func bulkClearCompleted(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		n, err := r.Bulk.ClearCompleted(req.Context())
		if err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", fmt.Sprintf("Cleared %d finished operation(s).", n))
		}
		http.Redirect(w, req, "/portal/bulk", http.StatusFound)
	}
}

func bulkProgress(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		op, _, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		p, err := r.Bulk.ProgressFor(req.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		finished := op.Status == model.BulkStatusCompleted || op.Status == model.BulkStatusCancelled
		writeJSONResp(w, map[string]any{
			"status":    op.Status,
			"percent":   p.Percent(),
			"total":     p.Total,
			"queued":    p.Queued,
			"running":   p.Running,
			"ok":        p.OK,
			"failed":    p.Failed,
			"cancelled": p.Cancelled,
			"finished":  finished,
		})
	}
}

func bulkDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		op, jobs, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		prog, _ := r.Bulk.ProgressFor(req.Context(), id)
		op.Progress = prog
		// Group jobs by run number (newest run first) for recurring history.
		type run struct {
			No   int
			Jobs []model.BulkJob
		}
		order := []int{}
		byRun := map[int]*run{}
		for _, j := range jobs {
			n := j.RunNo
			if byRun[n] == nil {
				byRun[n] = &run{No: n}
				order = append(order, n)
			}
			byRun[n].Jobs = append(byRun[n].Jobs, j)
		}
		// order ascending by appearance (jobs come ORDER BY id); present newest run first.
		runs := make([]*run, 0, len(order))
		for i := len(order) - 1; i >= 0; i-- {
			runs = append(runs, byRun[order[i]])
		}
		render(w, req, r, "bulk_detail.html", "Bulk operation #"+pathStr(req), map[string]any{
			"Op": op, "Jobs": jobs, "Runs": runs,
		})
	}
}

func pathStr(req *http.Request) string { return req.PathValue("id") }

// formPrefill builds the values the create/edit form needs to seed the action
// picker and schedule controls. op is nil for a new operation.
func formPrefill(op *model.BulkOperation) map[string]any {
	ap := map[string]any{
		"Name":              "",
		"Description":       "",
		"Action":            "rename",
		"RenameFind":        "",
		"RenameReplace":     "",
		"ScriptShell":       "powershell",
		"ScriptBody":        "",
		"SoftwarePackageID": int64(0),
		"ReimageImageID":    int64(0),
		"ScheduleKind":      "now",
		"TargetMode":        "selection",
		"RunAtLocal":        "",
		"RecurEvery":        1,
		"RecurUnit":         "week",
		"RecurAt":           "02:00",
		"RecurWeekday":      0,
		"RecurCron":         "",
		"FilterName":        "",
		"FilterOU":          "",
		"FilterGroup":       "",
		"WakeOnLAN":         false,
		"CancelAfterValue":  "",
		"CancelAfterUnit":   "hours",
	}
	if op == nil {
		return ap
	}
	ap["Name"], ap["Description"] = op.Name, op.Description
	ap["Action"] = op.Action
	switch op.Action {
	case model.BulkActionRename:
		var p struct{ Find, Replace string }
		var raw map[string]string
		_ = json.Unmarshal([]byte(op.Payload), &raw)
		p.Find, p.Replace = raw["rename_find"], raw["rename_replace"]
		ap["RenameFind"], ap["RenameReplace"] = p.Find, p.Replace
	case model.BulkActionScript:
		var raw map[string]string
		_ = json.Unmarshal([]byte(op.Payload), &raw)
		if raw["shell"] != "" {
			ap["ScriptShell"] = raw["shell"]
		}
		ap["ScriptBody"] = raw["body"]
	case model.BulkActionSoftwarePush:
		var raw map[string]int64
		_ = json.Unmarshal([]byte(op.Payload), &raw)
		ap["SoftwarePackageID"] = raw["package_id"]
	}
	ap["ReimageImageID"] = int64(op.ReimageImageID)
	ap["ScheduleKind"] = op.ScheduleKind
	if op.TargetMode != "" {
		ap["TargetMode"] = op.TargetMode
	}
	if op.RunAt != nil {
		ap["RunAtLocal"] = op.RunAt.Local().Format("2006-01-02T15:04")
	}
	if op.RecurSpec != "" {
		var rs model.RecurSpec
		_ = json.Unmarshal([]byte(op.RecurSpec), &rs)
		if rs.Every > 0 {
			ap["RecurEvery"] = rs.Every
		}
		if rs.Unit != "" {
			ap["RecurUnit"] = rs.Unit
		}
		if rs.At != "" {
			ap["RecurAt"] = rs.At
		}
		ap["RecurWeekday"] = rs.Weekday
		ap["RecurCron"] = rs.Cron
	}
	ap["FilterName"], ap["FilterOU"], ap["FilterGroup"] = op.Target.NameRegex, op.Target.OU, op.Target.Group
	ap["WakeOnLAN"] = op.WakeOnLAN
	if op.CancelAfterMinutes > 0 {
		// Render whole hours as hours, anything else in minutes.
		if op.CancelAfterMinutes%60 == 0 {
			ap["CancelAfterValue"] = strconv.Itoa(op.CancelAfterMinutes / 60)
			ap["CancelAfterUnit"] = "hours"
		} else {
			ap["CancelAfterValue"] = strconv.Itoa(op.CancelAfterMinutes)
			ap["CancelAfterUnit"] = "minutes"
		}
	}
	return ap
}

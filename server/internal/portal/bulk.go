package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
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
		post("/portal/bulk/review", bulkReview(r, false))
		post("/portal/bulk/{id}/review", bulkReview(r, true))
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
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		search := newSearchBox(req, "Search operations", "Search bulk operations")
		shown := filterByQuery(ops, search.Query, func(o model.BulkOperation) []string {
			return []string{o.Name, o.Description, o.Action, model.BulkActionLabel(o.Action),
				o.Status, bulkStatus(o).Label, o.CreatedBy}
		})
		render(w, req, r, "bulk_list.html", "Bulk operations", map[string]any{
			"Ops":    shown,
			"Search": search.counted(len(shown), len(ops), "operations"),
		})
	}
}

func bulkFormNew(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		pkgs, _ := r.Software.List(req.Context())
		imgs, _ := r.Images.List(req.Context())
		groups, _ := r.Groups.List(req.Context())
		render(w, req, r, "bulk_form.html", "New bulk operation", map[string]any{
			"Packages":    pkgs,
			"Images":      imgs,
			"Groups":      groups,
			"FormAction":   "/portal/bulk",
			"ReviewAction": "/portal/bulk/review",
			"Editing":      false,
			"AP":           formPrefill(nil, displayLoc(r)),
			"SelInitJSON":  "[]",
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
		if v := strings.TrimSpace(req.URL.Query().Get("group_id")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				t.GroupID = model.ID(n)
			}
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

// machineItems labels picker rows with the shared display name, loading the
// fleet's names once (not one binding query per machine).
func machineItems(r Repos, req *http.Request, machines []model.MachineRecord) []machineItem {
	names := namesFor(req, r)
	out := make([]machineItem, 0, len(machines))
	for _, m := range machines {
		name := model.MachineDisplayName(m, model.MachineBinding{}).Name
		if n, ok := names.Machine(m.ID); ok {
			name = n.Name
		}
		out = append(out, machineItem{
			ID: int64(m.ID), Name: name, UUID: m.SystemUUID,
			Make: m.SystemManufacturer, Product: m.SystemProduct,
		})
	}
	return out
}

// bulkParams collects the action payload, target and schedule from a create or
// edit form.
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

// bulkFieldErr is a parseBulkForm failure tied to the form field it
// concerns, so the re-rendered form can show it beside that field.
type bulkFieldErr struct{ Field, Msg string }

func (e *bulkFieldErr) Error() string { return e.Msg }

func fieldErr(field, msg string) error { return &bulkFieldErr{Field: field, Msg: msg} }

// parseBulkForm reads the shared action + target + schedule fields. On
// failure it returns a *bulkFieldErr naming the field and saying, in plain
// words, what to fix.
func parseBulkForm(req *http.Request, loc *time.Location) (bulkParams, error) {
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
		t, err := parseLocalDateTime(req.FormValue("run_at"), loc)
		if err != nil {
			return p, fieldErr("run_at", "Choose the date and time the operation should run.")
		}
		p.RunAt = &t
	case model.BulkScheduleRecurring:
		spec, err := recurSpecFromForm(req)
		if err != nil {
			return p, err
		}
		p.RecurSpec = spec
	default:
		return p, fieldErr("schedule_kind", "Choose when the operation runs: now, once or recurring.")
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
		if v := strings.TrimSpace(req.FormValue("filter_group_id")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				p.Target.GroupID = model.ID(n)
			}
		}
		if p.Target.NameRegex == "" && p.Target.OU == "" && p.Target.Group == "" && p.Target.GroupID == 0 {
			return p, fieldErr("filter_name", "A recurring task that looks machines up again each run needs at least one filter: name, OU, AD group or machine group.")
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
			return p, fieldErr("machine_ids", "Add at least one machine to the selection.")
		}
		p.Target = model.BulkTarget{MachineIDs: ids}
	}

	// Action payload (shared with single-machine actions, minus the
	// literal single-machine rename which bulk forbids).
	switch p.Action {
	case model.BulkActionRename:
		find := strings.TrimSpace(req.FormValue("rename_find"))
		if find == "" {
			return p, fieldErr("rename_find", "Enter the part of the name to find. To give one machine a new name, use its machine page.")
		}
		b, _ := json.Marshal(map[string]string{
			"rename_find":    find,
			"rename_replace": req.FormValue("rename_replace"),
		})
		p.Payload = string(b)
	case model.BulkActionScript:
		body := req.FormValue("script_body")
		if strings.TrimSpace(body) == "" {
			return p, fieldErr("script_body", "Enter the script to run.")
		}
		b, _ := json.Marshal(map[string]string{"shell": req.FormValue("script_shell"), "body": body})
		p.Payload = string(b)
	case model.BulkActionSoftwarePush:
		pid, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64)
		if err != nil || pid <= 0 {
			return p, fieldErr("software_package_id", "Choose the software package to install.")
		}
		b, _ := json.Marshal(map[string]int64{"package_id": pid})
		p.Payload = string(b)
	case model.BulkActionReimage:
		p.Payload = "{}"
		p.ReimageImageID = reimageImageIDFromForm(req)
	default:
		return p, fieldErr("action", "Choose what to do: rename, run a script, install software or re-image.")
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
		return 0, fieldErr("cancel_after_value", "Enter a whole number, or leave blank to wait indefinitely.")
	}
	switch req.FormValue("cancel_after_unit") {
	case "minutes":
		return n, nil
	case "", "hours":
		return n * 60, nil
	default:
		return 0, fieldErr("cancel_after_value", "Choose minutes or hours.")
	}
}

// parseLocalDateTime parses an <input type=datetime-local> value (no zone) in
// the configured display/scheduling timezone, so the operator's entered
// wall-clock means that zone (not the server's OS zone).
func parseLocalDateTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
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
		return "", fieldErr("recur_unit", "Choose how often the task repeats.")
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
		loc := displayLoc(r)
		p, err := parseBulkForm(req, loc)
		if err != nil {
			bulkFormRejected(w, req, r, nil, err)
			return
		}
		if fe := bulkConfirmError(req, r, p); fe != nil {
			bulkReviewRender(w, req, r, nil, p, fe)
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
			bulkFormRejected(w, req, r, nil, err)
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
			flash(w, "ok", "Operation scheduled for "+op.RunAt.In(loc).Format("Mon 2 Jan 15:04")+".")
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
			failPage(w, req, r, http.StatusNotFound, err)
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
		groups, _ := r.Groups.List(req.Context())
		render(w, req, r, "bulk_form.html", "Edit "+bulkTitle(op), map[string]any{
			"Packages":    pkgs,
			"Images":      imgs,
			"Groups":      groups,
			"FormAction":   fmt.Sprintf("/portal/bulk/%d/edit", int64(id)),
			"ReviewAction": fmt.Sprintf("/portal/bulk/%d/review", int64(id)),
			"Editing":      true,
			"Op":           op,
			"AP":           formPrefill(&op, displayLoc(r)),
			"SelInitJSON":  string(selJSON),
		})
	}
}

func bulkEditSave(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := parseBulkForm(req, displayLoc(r))
		if err != nil {
			bulkEditRejected(w, req, r, id, err)
			return
		}
		if fe := bulkConfirmError(req, r, p); fe != nil {
			op, _, gerr := r.Bulk.GetOperation(req.Context(), id)
			if gerr != nil {
				failPage(w, req, r, http.StatusNotFound, gerr)
				return
			}
			bulkReviewRender(w, req, r, &op, p, fe)
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
			bulkEditRejected(w, req, r, id, err)
			return
		}
		flash(w, "ok", "Operation updated.")
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", int64(id)), http.StatusFound)
	}
}

// bulkEditRejected re-renders a scheduled operation's edit form after a
// failed save.
func bulkEditRejected(w http.ResponseWriter, req *http.Request, r Repos, id model.ID, err error) {
	op, _, gerr := r.Bulk.GetOperation(req.Context(), id)
	if gerr != nil {
		failPage(w, req, r, http.StatusNotFound, gerr)
		return
	}
	bulkFormRejected(w, req, r, &op, err)
}

// bulkFormRejected re-renders the bulk form (new, or editing op) with
// everything the operator submitted — read straight from the request, so
// it works whichever field failed — plus the error beside its field, with
// HTTP 422.
func bulkFormRejected(w http.ResponseWriter, req *http.Request, r Repos, op *model.BulkOperation, err error) {
	bulkFormRender(w, req, r, op, bulkFormError(req, err))
}

// bulkFormError maps a parse or save failure onto the bulk form's fields.
func bulkFormError(req *http.Request, err error) *FormError {
	const top = "Couldn't save — fix the highlighted field below. Nothing was queued."
	var fieldFail *bulkFieldErr
	if errors.As(err, &fieldFail) {
		slog.Info("portal.form.invalid", slog.String("path", req.URL.Path),
			slog.String("kind", model.KindBulk), slog.String("error", err.Error()))
		return &FormError{Message: top, Fields: map[string]string{fieldFail.Field: fieldFail.Msg}}
	}
	fe := newFormError(req, model.KindBulk, err)
	msg := friendlyError(model.KindBulk, http.StatusUnprocessableEntity, err)
	// The model calls the name filter "name pattern"; the bulk form's field
	// is filter_name. Schedule problems belong beside the schedule fields.
	if m := fe.Fields["name_regex"]; m != "" {
		delete(fe.Fields, "name_regex")
		fe.Fields["filter_name"] = m
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "cron"):
		fe.Fields["recur_cron"] = msg
	case strings.Contains(low, "run time"):
		fe.Fields["run_at"] = msg
	}
	if len(fe.Fields) > 0 {
		fe.Message = top
	} else {
		fe.Message = "Couldn't save: " + strings.TrimSuffix(msg, ".") + ". Nothing was queued."
	}
	return fe
}

// bulkFormRender renders the bulk form from the submitted request: after a
// rejected save (fe set, 422) or when the review step's "Back to edit"
// returns to it (fe nil, every input kept).
func bulkFormRender(w http.ResponseWriter, req *http.Request, r Repos, op *model.BulkOperation, fe *FormError) {
	pkgs, _ := r.Software.List(req.Context())
	imgs, _ := r.Images.List(req.Context())
	groups, _ := r.Groups.List(req.Context())
	var sel []machineItem
	if ids := formIDs(req.Form["machine_ids"]); len(ids) > 0 {
		ms, _ := r.Bulk.PreviewTargets(req.Context(), model.BulkTarget{MachineIDs: ids})
		sel = machineItems(r, req, ms)
	}
	selJSON, _ := json.Marshal(sel)
	if sel == nil {
		selJSON = []byte("[]")
	}
	data := map[string]any{
		"Packages":     pkgs,
		"Images":       imgs,
		"Groups":       groups,
		"FormAction":   "/portal/bulk",
		"ReviewAction": "/portal/bulk/review",
		"Editing":      false,
		"AP":           bulkPrefillFromRequest(req),
		"SelInitJSON":  string(selJSON),
	}
	title := "New bulk operation"
	if op != nil {
		data["FormAction"] = fmt.Sprintf("/portal/bulk/%d/edit", int64(op.ID))
		data["ReviewAction"] = fmt.Sprintf("/portal/bulk/%d/review", int64(op.ID))
		data["Editing"] = true
		data["Op"] = *op
		title = "Edit " + bulkTitle(*op)
	}
	renderForm(w, req, r, "bulk_form.html", title, fe, data)
}

// bulkPrefillFromRequest is formPrefill's view of a submitted form: the
// same keys and value types, taken from the posted fields.
func bulkPrefillFromRequest(req *http.Request) map[string]any {
	ap := formPrefill(nil, time.UTC)
	str := func(key, field string) {
		if v := req.FormValue(field); v != "" {
			ap[key] = v
		}
	}
	num := func(key, field string) {
		if n, err := strconv.ParseInt(strings.TrimSpace(req.FormValue(field)), 10, 64); err == nil {
			ap[key] = n
		}
	}
	ap["Name"] = strings.TrimSpace(req.FormValue("name"))
	ap["Description"] = strings.TrimSpace(req.FormValue("description"))
	str("Action", "action")
	str("RenameFind", "rename_find")
	str("RenameReplace", "rename_replace")
	str("ScriptShell", "script_shell")
	ap["ScriptBody"] = req.FormValue("script_body")
	num("SoftwarePackageID", "software_package_id")
	num("ReimageImageID", "reimage_image_id")
	str("ScheduleKind", "schedule_kind")
	str("TargetMode", "target_mode")
	str("RunAtLocal", "run_at")
	if n, err := strconv.Atoi(strings.TrimSpace(req.FormValue("recur_every"))); err == nil && n > 0 {
		ap["RecurEvery"] = n
	}
	str("RecurUnit", "recur_unit")
	str("RecurAt", "recur_at")
	if n, err := strconv.Atoi(req.FormValue("recur_weekday")); err == nil {
		ap["RecurWeekday"] = n
	}
	str("RecurCron", "recur_cron")
	str("FilterName", "filter_name")
	str("FilterOU", "filter_ou")
	str("FilterGroup", "filter_group")
	num("FilterGroupID", "filter_group_id")
	ap["WakeOnLAN"] = req.FormValue("wake_on_lan") != ""
	str("CancelAfterValue", "cancel_after_value")
	str("CancelAfterUnit", "cancel_after_unit")
	return ap
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
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		prog, _ := r.Bulk.ProgressFor(req.Context(), id)
		op.Progress = prog
		// Resolve each job's machine ID to a display name so the table shows
		// names, not bare IDs. The request's name maps load once; the template
		// links each job via entityLink, and MachineName stays for callers
		// that read the view directly.
		lookup := namesFor(req, r)
		names := map[model.ID]string{}
		for _, j := range jobs {
			if n, ok := lookup.Machine(j.MachineID); ok {
				names[j.MachineID] = n.Name
			}
		}
		// Group jobs by run number (newest run first) for recurring history.
		type jobView struct {
			model.BulkJob
			MachineName string
		}
		type run struct {
			No   int
			Jobs []jobView
		}
		order := []int{}
		byRun := map[int]*run{}
		for _, j := range jobs {
			n := j.RunNo
			if byRun[n] == nil {
				byRun[n] = &run{No: n}
				order = append(order, n)
			}
			byRun[n].Jobs = append(byRun[n].Jobs, jobView{BulkJob: j, MachineName: names[j.MachineID]})
		}
		// order ascending by appearance (jobs come ORDER BY id); present newest run first.
		runs := make([]*run, 0, len(order))
		for i := len(order) - 1; i >= 0; i-- {
			runs = append(runs, byRun[order[i]])
		}
		// Name what the operation acts on, not just its payload JSON: the
		// re-image target image and the pushed software package.
		var image, pkg template.HTML
		switch op.Action {
		case model.BulkActionReimage:
			if op.ReimageImageID != 0 {
				image = entityLinkHTML(lookup, model.KindImage, op.ReimageImageID)
			} else {
				image = template.HTML(`<span class="muted">each machine's bound image</span>`)
			}
		}
		detail := decodeBulkPayload(op.Action, op.Payload)
		if detail.PackageID > 0 {
			pkg = entityLinkHTML(lookup, model.KindSoftware, model.ID(detail.PackageID))
		}
		render(w, req, r, "bulk_detail.html", bulkTitle(op), map[string]any{
			"Op": op, "Jobs": jobs, "Runs": runs,
			"Image": image, "Package": pkg,
			"Detail": detail,
			"Target": bulkTargetHTML(lookup, op.Target, op.TargetMode),
		})
	}
}

func pathStr(req *http.Request) string { return req.PathValue("id") }

// bulkTitle is a bulk operation's page title: its name, or "<Action> #ID"
// for rows created before operations had names.
func bulkTitle(op model.BulkOperation) string {
	if strings.TrimSpace(op.Name) != "" {
		return op.Name
	}
	return fmt.Sprintf("%s #%d", model.BulkActionLabel(op.Action), int64(op.ID))
}

// formPrefill builds the values the create/edit form needs to seed the action
// picker and schedule controls. op is nil for a new operation. loc is the
// configured display/scheduling zone the datetime-local control is filled in,
// so editing round-trips the wall-clock the operator originally entered.
func formPrefill(op *model.BulkOperation, loc *time.Location) map[string]any {
	ap := map[string]any{
		"Name":              "",
		"Description":       "",
		// No default action: the operator must pick one (a pre-selected
		// action is one click from queuing the wrong thing fleet-wide).
		"Action":            "",
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
		"FilterGroupID":     int64(0),
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
		ap["RunAtLocal"] = op.RunAt.In(loc).Format("2006-01-02T15:04")
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
	ap["FilterGroupID"] = int64(op.Target.GroupID)
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

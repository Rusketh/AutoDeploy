package portal

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// The bulk form's review step. "Review" on the form posts here instead of
// straight to /portal/bulk: the same fields are parsed and validated, and
// the page summarises — in words, with machine names — what will happen
// before anything is queued. Its "Queue operation" button posts every
// field unchanged (as hidden inputs) to the original create/edit URL, so
// that POST is the same one scripts and older pages send without a review
// step. Re-image additionally asks the operator to type the machine count.

// Review-only fields: never echoed into the final POST's hidden inputs.
const (
	reviewedField     = "reviewed"      // set by the review page's final submit
	confirmCountField = "confirm_count" // typed machine count for a re-image
	reviewBackField   = "review_back"   // "Back to edit"
)

// hiddenField is one name/value pair the review page re-posts.
type hiddenField struct{ Name, Value string }

// bulkReviewView is what bulk_review.html shows.
type bulkReviewView struct {
	Editing      bool
	FormAction   string // final POST (create or edit)
	ReviewAction string // "Back to edit" target
	Name         string
	Description  string
	Headline     string        // "Re-image 24 machines, every Saturday at 02:00 (UTC)"
	ActionLabel  string        // "Re-image"
	ActionDetail template.HTML // what exactly the action does
	Destructive  bool
	Count        int
	Machines     template.HTML // names, first 20 + "and N more"
	Filter       bool          // target is looked up again each run
	FilterText   template.HTML
	When         string
	WakeOnLAN    bool
	CancelAfter  string
	Hidden       []hiddenField
}

// bulkReview handles POST /portal/bulk/review and /portal/bulk/{id}/review.
func bulkReview(r Repos, editing bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var op *model.BulkOperation
		if editing {
			id, _ := pathID(req)
			o, _, err := r.Bulk.GetOperation(req.Context(), id)
			if err != nil {
				failPage(w, req, r, http.StatusNotFound, err)
				return
			}
			op = &o
		}
		p, err := parseBulkForm(req, displayLoc(r))
		if req.FormValue(reviewBackField) != "" {
			// Back to edit: the form again, every input as submitted.
			bulkFormRender(w, req, r, op, nil)
			return
		}
		if err != nil {
			bulkFormRender(w, req, r, op, bulkFormError(req, err))
			return
		}
		bulkReviewRender(w, req, r, op, p, nil)
	}
}

// bulkTargetRecords resolves what an operation would act on right now —
// the same lookup CreateOperation makes.
func bulkTargetRecords(req *http.Request, r Repos, t model.BulkTarget) []model.MachineRecord {
	ms, _ := r.Bulk.PreviewTargets(req.Context(), t)
	return ms
}

// bulkConfirmError checks the typed machine count when the review page
// submitted a re-image. Posts without the review step (scripts, API-style
// callers, older pages) carry no "reviewed" field and are not asked.
func bulkConfirmError(req *http.Request, r Repos, p bulkParams) *FormError {
	if req.FormValue(reviewedField) == "" || p.Action != model.BulkActionReimage {
		return nil
	}
	n := len(bulkTargetRecords(req, r, p.Target))
	if strings.TrimSpace(req.FormValue(confirmCountField)) == strconv.Itoa(n) {
		return nil
	}
	return &FormError{
		Message: "Nothing was queued — type the number of machines to confirm the re-image.",
		Fields:  map[string]string{confirmCountField: fmt.Sprintf("Type %d to confirm.", n)},
	}
}

// bulkReviewRender renders the review page for validated params; fe is set
// when the final submit was refused (wrong confirmation count) — 422.
func bulkReviewRender(w http.ResponseWriter, req *http.Request, r Repos, op *model.BulkOperation, p bulkParams, fe *FormError) {
	v := buildBulkReview(req, r, p)
	v.FormAction, v.ReviewAction = "/portal/bulk", "/portal/bulk/review"
	title := "Review bulk operation"
	if op != nil {
		v.Editing = true
		v.FormAction = fmt.Sprintf("/portal/bulk/%d/edit", int64(op.ID))
		v.ReviewAction = fmt.Sprintf("/portal/bulk/%d/review", int64(op.ID))
		title = "Review changes to " + bulkTitle(*op)
	}
	renderForm(w, req, r, "bulk_review.html", title, fe, map[string]any{"Review": v})
}

// buildBulkReview describes validated params in words.
func buildBulkReview(req *http.Request, r Repos, p bulkParams) bulkReviewView {
	loc := displayLoc(r)
	names := namesFor(req, r)
	esc := template.HTMLEscapeString
	v := bulkReviewView{
		Name:        p.Name,
		Description: p.Description,
		ActionLabel: model.BulkActionLabel(p.Action),
		Destructive: p.Action == model.BulkActionReimage,
		WakeOnLAN:   p.WakeOnLAN,
		CancelAfter: model.BulkOperation{CancelAfterMinutes: p.CancelAfterMinutes}.CancelAfterLabel(),
		Hidden:      reviewHiddenFields(req),
	}

	// What the action does.
	d := decodeBulkPayload(p.Action, p.Payload)
	switch p.Action {
	case model.BulkActionRename:
		repl := "<em>nothing</em>"
		if d.Replace != "" {
			repl = "<code>" + esc(d.Replace) + "</code>"
		}
		v.ActionDetail = template.HTML("Replace <code>" + esc(d.Find) + "</code> with " + repl + " in each machine's name. Renamed machines restart.")
	case model.BulkActionScript:
		v.ActionDetail = template.HTML("Run this " + esc(d.ShellLabel) + " on each machine:<pre class=\"code-block mt-1\">" + esc(d.Body) + "</pre>")
	case model.BulkActionSoftwarePush:
		v.ActionDetail = "Install " + entityLinkHTML(names, model.KindSoftware, model.ID(d.PackageID)) +
			" (and anything it depends on). Machines that already have it are skipped."
	case model.BulkActionReimage:
		img := template.HTML("the image each machine is already assigned")
		if p.ReimageImageID != 0 {
			img = entityLinkHTML(names, model.KindImage, p.ReimageImageID)
		}
		v.ActionDetail = "Wipe each machine and install " + img + ". Machines restart into network boot at their next check-in; everything on their disks is erased."
	}

	// Which machines.
	records := bulkTargetRecords(req, r, p.Target)
	v.Count = len(records)
	items := machineItems(r, req, records)
	parts := make([]template.HTML, 0, min(len(items), namesShown))
	for i, m := range items {
		if i == namesShown {
			break
		}
		parts = append(parts, template.HTML(esc(m.Name)))
	}
	v.Machines = joinNamesHTML(parts, len(items))
	if p.TargetMode == model.BulkTargetFilter {
		v.Filter = true
		v.FilterText = bulkTargetHTML(names, p.Target, p.TargetMode)
	}

	// When.
	switch p.ScheduleKind {
	case model.BulkScheduleOnce:
		v.When = "Once, at " + p.RunAt.In(loc).Format("Mon 2 Jan 2006 15:04") + " (" + loc.String() + ")"
	case model.BulkScheduleRecurring:
		v.When = "Repeats " + recurText(p.RecurSpec, loc)
	default:
		v.When = "Now — each machine starts at its next check-in"
	}

	machines := fmt.Sprintf("%d machine%s", v.Count, plural(v.Count))
	when := "starting now"
	switch p.ScheduleKind {
	case model.BulkScheduleOnce:
		when = "on " + p.RunAt.In(loc).Format("Mon 2 Jan at 15:04") + " (" + loc.String() + ")"
	case model.BulkScheduleRecurring:
		when = recurText(p.RecurSpec, loc)
	}
	switch p.Action {
	case model.BulkActionScript:
		v.Headline = "Run a script on " + machines
	case model.BulkActionSoftwarePush:
		pkg := "software"
		if n, ok := names.Name(model.KindSoftware, model.ID(d.PackageID)); ok {
			pkg = n
		}
		v.Headline = "Install " + pkg + " on " + machines
	default: // "Rename 3 machines", "Re-image 24 machines"
		v.Headline = v.ActionLabel + " " + machines
	}
	v.Headline += ", " + when
	return v
}

// reviewHiddenFields is every submitted field except the review step's own,
// in a stable order, for the final POST.
func reviewHiddenFields(req *http.Request) []hiddenField {
	keys := make([]string, 0, len(req.PostForm))
	for k := range req.PostForm {
		switch k {
		case reviewedField, confirmCountField, reviewBackField:
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []hiddenField
	for _, k := range keys {
		for _, v := range req.PostForm[k] {
			out = append(out, hiddenField{Name: k, Value: v})
		}
	}
	return out
}

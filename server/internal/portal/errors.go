package portal

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// This file turns handler errors into operator-facing pages and flashes.
// Raw Go errors ("image 99999: not found") are logged via slog and never
// shown as a bare text/plain page: failPage renders the styled in-layout
// error page with a plain sentence and a link back to the relevant list.

// listSection describes the portal section a URL belongs to: the entity
// kind it manages and the list page to send the operator back to.
type listSection struct {
	prefix string
	kind   string // model.Kind* ("" for non-entity sections)
	url    string // list page
	label  string // list page name, for "Back to <label>"
}

// listSections is matched longest-prefix-first by sectionFor.
var listSections = []listSection{
	{"/portal/update-deployments", "", "/portal/updates", "Windows Updates"},
	{"/portal/images/groups", model.KindImageGroup, "/portal/images/groups", "Image groups"},
	{"/portal/images", model.KindImage, "/portal/images", "Images"},
	{"/portal/isos", model.KindISO, "/portal/isos", "ISOs"},
	{"/portal/unattends", model.KindUnattend, "/portal/unattends", "Unattends"},
	{"/portal/drivers", model.KindDriver, "/portal/drivers", "Drivers"},
	{"/portal/software", model.KindSoftware, "/portal/software", "Software"},
	{"/portal/loadouts", model.KindLoadout, "/portal/loadouts", "Loadouts"},
	{"/portal/machines", model.KindMachine, "/portal/machines", "Machines"},
	{"/portal/groups", model.KindGroup, "/portal/groups", "Groups"},
	{"/portal/bulk", model.KindBulk, "/portal/bulk", "Bulk operations"},
	{"/portal/import", "", "/portal/import", "Import"},
	{"/portal/updates", model.KindUpdate, "/portal/updates", "Windows Updates"},
	{"/portal/mirrors", model.KindMirror, "/portal/mirrors", "Mirrors"},
	{"/portal/logs", "", "/portal/logs", "Logs"},
	{"/portal/notifications", "", "/portal/notifications", "Notifications"},
	{"/portal/settings/notifications/webhooks", model.KindWebhook, "/portal/settings/notifications", "Notification settings"},
	{"/portal/settings", "", "/portal/settings", "Settings"},
	{"/portal/downloads", "", "/portal/downloads", "Downloads"},
}

// sectionFor returns the section a request path belongs to (the dashboard
// when none matches).
func sectionFor(path string) listSection {
	best := listSection{url: "/portal/", label: "Dashboard"}
	for _, s := range listSections {
		if pathUnder(path, s.prefix) && len(s.prefix) > len(best.prefix) {
			best = s
		}
	}
	return best
}

// friendlyError is the plain sentence shown to the operator for err. kind
// names the entity the request was about ("" when unknown).
func friendlyError(kind string, status int, err error) string {
	noun := kindNoun(kind)
	var inUse *model.InUseError
	switch {
	case errors.As(err, &inUse):
		return inUse.Friendly()
	case errors.Is(err, model.ErrNotFound):
		if kind == "" {
			return "That item no longer exists. It may have been deleted."
		}
		return "That " + noun + " no longer exists. It may have been deleted."
	case errors.Is(err, model.ErrValidation):
		msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), model.ErrValidation.Error()+":"))
		if msg == "" || msg == err.Error() {
			// Wrapped the other way round ("... : validation failed").
			msg = strings.TrimSpace(strings.Replace(err.Error(), model.ErrValidation.Error(), "", 1))
			msg = strings.Trim(msg, ": ")
		}
		if msg == "" {
			return "The submitted values aren't valid."
		}
		return upperFirst(msg) + "."
	case errors.Is(err, model.ErrConflict):
		return "That conflicts with an existing " + noun + ". Names must be unique."
	case errors.Is(err, model.ErrCycle):
		return "That parent would create an inheritance loop. Choose a different parent."
	}
	switch {
	case status == http.StatusNotFound:
		return "That " + noun + " doesn't exist. The link may be out of date."
	case status >= 500:
		return "Something went wrong on the server. The details were written to the server log."
	case status == http.StatusBadRequest && strings.HasPrefix(err.Error(), "invalid id"):
		return "That address doesn't point to a valid " + noun + "."
	case status >= 400:
		// Hand-written handler messages ("choose an image to set", "bad
		// form") are already operator-facing.
		return upperFirst(err.Error()) + "."
	}
	return "The request couldn't be completed."
}

func upperFirst(s string) string {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// errorTitle is the error page heading for a status.
func errorTitle(kind string, status int, err error) string {
	switch {
	case errors.Is(err, model.ErrNotFound) || status == http.StatusNotFound:
		if kind != "" {
			return upperFirst(kindNoun(kind)) + " not found"
		}
		return "Not found"
	case errors.Is(err, model.ErrInUse):
		return "Still in use"
	case status >= 500:
		return "Something went wrong"
	}
	return "That didn't work"
}

// isBackgroundRequest reports whether req came from page script (fetch /
// XHR upload) rather than a browser navigation. Those callers display the
// response text themselves, so they keep the plain-text body.
func isBackgroundRequest(req *http.Request) bool {
	if m := req.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m != "navigate"
	}
	return req.Header.Get("X-Requested-With") != ""
}

// failPage reports a failed portal request: it logs the raw error, then
// renders the styled in-layout error page (status preserved) with a plain
// message and a link back to the list the URL belongs to. Script-initiated
// requests get the old plain-text response so upload progress UIs keep
// showing the error inline. Use instead of http.Error(w, err.Error(), …)
// in portal handlers; /api/... handlers keep their JSON errors.
func failPage(w http.ResponseWriter, req *http.Request, r Repos, status int, err error) {
	if err == nil {
		err = errors.New(http.StatusText(status))
	}
	sec := sectionFor(req.URL.Path)
	level := slog.LevelInfo
	if status >= 500 {
		level = slog.LevelError
	}
	slog.Log(req.Context(), level, "portal.request.failed",
		slog.String("path", req.URL.Path), slog.String("method", req.Method),
		slog.Int("status", status), slog.String("error", err.Error()))
	if isBackgroundRequest(req) {
		http.Error(w, err.Error(), status)
		return
	}
	detail := ""
	if status < 500 {
		detail = err.Error() // safe to show 4xx detail; 5xx detail stays in the log
	}
	sw := &statusWriter{ResponseWriter: w, status: status}
	render(sw, req, r, "error.html", errorTitle(sec.kind, status, err), map[string]any{
		"Code":      status,
		"Heading":   errorTitle(sec.kind, status, err),
		"Message":   friendlyError(sec.kind, status, err),
		"Detail":    detail,
		"BackURL":   sec.url,
		"BackLabel": sec.label,
	})
}

// deleteFlash sets a delete handler's outcome flash: `Deleted "Win11".` on
// success, the friendly in-use / not-found sentence on failure (raw error
// logged). name is the object's name read before the delete ("" if unknown).
func deleteFlash(w http.ResponseWriter, req *http.Request, kind, name string, err error) {
	if err == nil {
		if name == "" {
			flash(w, "ok", "Deleted.")
			return
		}
		flash(w, "ok", "Deleted \""+name+"\".")
		return
	}
	slog.Info("portal.delete.failed", slog.String("path", req.URL.Path),
		slog.String("kind", kind), slog.String("error", err.Error()))
	var inUse *model.InUseError
	if errors.As(err, &inUse) {
		if inUse.Name == "" {
			inUse.Name = name
		}
		flash(w, "err", inUse.Friendly())
		return
	}
	msg := friendlyError(kind, http.StatusBadRequest, err)
	if name != "" && !errors.Is(err, model.ErrNotFound) {
		msg = "Couldn't delete \"" + name + "\": " + msg
	}
	flash(w, "err", msg)
}

// nameBeforeDelete reads an entity's name for the delete flash.
func nameBeforeDelete(req *http.Request, r Repos, kind string, id model.ID) string {
	n, _ := namesFor(req, r).Name(kind, id)
	return n
}

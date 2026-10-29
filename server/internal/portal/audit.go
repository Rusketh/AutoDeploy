package portal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// The portal's audit trail: every state-changing portal form POST listed in
// auditRoutes writes one log_event row naming the signed-in user, what they
// changed and whether it worked. Recording happens in a wrapper around the
// handler (see audited), so individual handlers don't need audit code; the
// result is read from what the handler did — its redirect and the flash
// message it set — rather than from its internals.

// auditSpec describes one audited route.
type auditSpec struct {
	// Action is the log action, "<kind>.<verb>" (see model.AuditNouns) or
	// one of the special actions below.
	Action string
	// Section names the settings page for settings.update.
	Section string
	// NameField is the form field holding the object's name ("name").
	NameField string
}

// auditRoutes maps a portal POST route (as registered) to its audit entry.
// Uploads that stream through XHR log their own events (logging.Event) and
// test / preview buttons change nothing, so neither is listed.
var auditRoutes = map[string]auditSpec{
	"/portal/bulk":                                        {Action: "bulk.create", NameField: "name"},
	"/portal/bulk/clear-completed":                        {Action: "bulk.clear"},
	"/portal/bulk/{id}/cancel":                            {Action: "bulk.cancel"},
	"/portal/bulk/{id}/edit":                              {Action: "bulk.update", NameField: "name"},
	"/portal/bulk/{id}/pause":                             {Action: "bulk.pause"},
	"/portal/bulk/{id}/resume":                            {Action: "bulk.resume"},
	"/portal/bulk/{id}/run-now":                           {Action: "bulk.run_now"},
	"/portal/drivers":                                     {Action: "driver.create", NameField: "name"},
	"/portal/drivers/{id}":                                {Action: "driver.update", NameField: "name"},
	"/portal/drivers/{id}/delete":                         {Action: "driver.delete"},
	"/portal/drivers/{id}/extract":                        {Action: "driver.extract"},
	"/portal/drivers/{id}/upload":                         {Action: "driver.upload"},
	"/portal/images":                                      {Action: "image.create", NameField: "name"},
	"/portal/images/groups":                               {Action: "image_group.create", NameField: "name"},
	"/portal/images/groups/{id}/delete":                   {Action: "image_group.delete"},
	"/portal/images/groups/{id}/save":                     {Action: "image_group.update", NameField: "name"},
	"/portal/images/{id}":                                 {Action: "image.update", NameField: "name"},
	"/portal/images/{id}/clone":                           {Action: "image.clone"},
	"/portal/images/{id}/delete":                          {Action: "image.delete"},
	"/portal/isos":                                        {Action: "iso.create", NameField: "name"},
	"/portal/isos/{id}":                                   {Action: "iso.update", NameField: "name"},
	"/portal/isos/{id}/delete":                            {Action: "iso.delete"},
	"/portal/isos/{id}/extract":                           {Action: "iso.extract"},
	"/portal/isos/{id}/upload":                            {Action: "iso.upload"},
	"/portal/loadouts":                                    {Action: "loadout.create", NameField: "name"},
	"/portal/loadouts/{id}":                               {Action: "loadout.update", NameField: "name"},
	"/portal/loadouts/{id}/clone":                         {Action: "loadout.clone"},
	"/portal/loadouts/{id}/delete":                        {Action: "loadout.delete"},
	"/portal/machines/delete":                             {Action: "machine.bulk_delete"},
	"/portal/machines/group-membership":                   {Action: "machine.group_membership"},
	"/portal/machines/{id}/action":                        {Action: "machine.action"},
	"/portal/machines/{id}/binding":                       {Action: "machine.binding.update"},
	"/portal/machines/{id}/delete":                        {Action: "machine.delete"},
	"/portal/machines/{id}/reimage/cancel":                {Action: "machine.reimage.cancel"},
	"/portal/mirrors":                                     {Action: "mirror.create", NameField: "name"},
	"/portal/mirrors/{id}":                                {Action: "mirror.update", NameField: "name"},
	"/portal/mirrors/{id}/delete":                         {Action: "mirror.delete"},
	"/portal/mirrors/{id}/health":                         {Action: "mirror.update"},
	"/portal/settings/access-pin":                         {Action: "settings.update", Section: "access PIN"},
	"/portal/settings/accounts":                           {Action: "account.create", NameField: "username"},
	"/portal/settings/accounts/{id}/delete":               {Action: "account.delete"},
	"/portal/settings/accounts/{id}/disable":              {Action: "account.disable"},
	"/portal/settings/accounts/{id}/enable":               {Action: "account.enable"},
	"/portal/settings/accounts/{id}/password":             {Action: "account.password"},
	"/portal/settings/active-directory":                   {Action: "settings.update", Section: "Active Directory"},
	"/portal/settings/branding":                           {Action: "settings.update", Section: "branding"},
	"/portal/settings/network":                            {Action: "settings.update", Section: "network"},
	"/portal/settings/network/upload-cert":                {Action: "settings.update", Section: "TLS certificate"},
	"/portal/settings/notifications":                      {Action: "settings.update", Section: "notification"},
	"/portal/settings/notifications/preferences":          {Action: "settings.update", Section: "notification preference"},
	"/portal/settings/notifications/webhooks/new":         {Action: "webhook.create", NameField: "name"},
	"/portal/settings/notifications/webhooks/{id}":        {Action: "webhook.update", NameField: "name"},
	"/portal/settings/notifications/webhooks/{id}/delete": {Action: "webhook.delete"},
	"/portal/settings/operational":                        {Action: "settings.update", Section: "operational"},
	"/portal/settings/storage":                            {Action: "settings.update", Section: "storage"},
	"/portal/software":                                    {Action: "software.create", NameField: "name"},
	"/portal/software/{id}":                               {Action: "software.update", NameField: "name"},
	"/portal/software/{id}/delete":                        {Action: "software.delete"},
	"/portal/software/{id}/bundle/{name}/delete":          {Action: "software.file_delete"},
	"/portal/software/{id}/files/delete":                  {Action: "software.file_delete"},
	"/portal/software/{id}/upload/delete":                 {Action: "software.file_delete"},
	"/portal/unattends":                                   {Action: "unattend.create", NameField: "name"},
	"/portal/unattends/{id}":                              {Action: "unattend.update", NameField: "name"},
	"/portal/unattends/{id}/clone":                        {Action: "unattend.clone"},
	"/portal/unattends/{id}/delete":                       {Action: "unattend.delete"},
	"/portal/update-deployments":                          {Action: "update_deployment.create"},
	"/portal/updates":                                     {Action: "update.create", NameField: "kb_number"},
	"/portal/updates/{id}":                                {Action: "update.update", NameField: "kb_number"},
	"/portal/updates/{id}/delete":                         {Action: "update.delete"},
}

// auditVerbBase is the bare verb for "tried to … — not saved" messages.
var auditVerbBase = map[string]string{
	"run_now": "start", "password": "reset the password of", "file_delete": "delete a file from",
	"extract": "extract", "upload": "upload files to", "bulk_delete": "delete",
	"group_membership": "change group membership of", "action": "queue an action on",
	"reimage": "re-image", "binding.update": "update the binding of",
	"reimage.cancel": "cancel the re-image of",
}

// auditWriter records the status a handler wrote.
type auditWriter struct {
	http.ResponseWriter
	status int
}

func (a *auditWriter) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *auditWriter) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return a.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (flushing a
// long upload's progress, deadlines).
func (a *auditWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// audited wraps a portal POST handler with its audit entry, or returns h
// unchanged when the route isn't audited.
func audited(r Repos, path string, h http.HandlerFunc) http.HandlerFunc {
	spec, ok := auditRoutes[path]
	if !ok || r.Logs == nil {
		return h
	}
	return func(w http.ResponseWriter, req *http.Request) {
		kind, _, _ := strings.Cut(spec.Action, ".")
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		// The name as it was before the change: the only way to name a
		// deleted row later, and the old name of a renamed one.
		before := ""
		if id > 0 {
			before = auditObjectName(req, r, kind, model.ID(id))
		}
		aw := &auditWriter{ResponseWriter: w}
		h(aw, req)
		recordAudit(req, r, spec, kind, model.ID(id), before, aw)
	}
}

// auditObjectName looks up an object's current display name ("" if none).
func auditObjectName(req *http.Request, r Repos, kind string, id model.ID) string {
	switch kind {
	case "account":
		if r.Users != nil {
			if u, err := r.Users.GetUser(req.Context(), int64(id)); err == nil {
				return u.Username
			}
		}
		return ""
	case "machine":
		if n, ok := namesFor(req, r).Machine(id); ok {
			return n.Name
		}
		return ""
	}
	if model.NormalizeKind(kind) == "" {
		return ""
	}
	if n, ok := namesFor(req, r).Name(kind, id); ok {
		return n
	}
	return ""
}

var auditCreatedID = regexp.MustCompile(`^/portal/[a-z/-]+?/(\d+)(?:/|$|\?)`)

func recordAudit(req *http.Request, r Repos, spec auditSpec, kind string, id model.ID, before string, aw *auditWriter) {
	status := aw.status
	if status == 0 {
		status = http.StatusOK
	}
	flashKind, flashMsg := flashFromHeader(aw.Header())
	failed := status >= 400 || flashKind == "err"
	action := spec.Action
	_, verb, _ := strings.Cut(action, ".")

	// The object: from the route, or — for a create — from the redirect.
	if id == 0 && strings.HasSuffix(action, ".create") && !failed {
		if m := auditCreatedID.FindStringSubmatch(aw.Header().Get("Location")); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			id = model.ID(n)
		}
	}
	f := map[string]any{}
	name := before
	if spec.NameField != "" {
		if v := strings.TrimSpace(req.PostFormValue(spec.NameField)); v != "" {
			if before != "" && before != v {
				f["previous_name"] = before
			}
			name = v
		}
	}
	if name != "" {
		f["name"] = name
	}
	if spec.Section != "" {
		f["section"] = spec.Section
	}
	switch action {
	case "machine.action":
		a := req.PostFormValue("action")
		f["action"] = model.BulkActionLabel(a)
		if a == model.BulkActionReimage {
			action, verb = "machine.reimage.request", "reimage"
		}
	case "machine.bulk_delete", "machine.group_membership":
		f["count"] = len(req.PostForm["machine_ids"])
	case "update_deployment.create":
		f["count"] = len(req.PostForm["update_ids"])
	}
	if failed {
		f["status"] = status
		if flashKind == "err" && flashMsg != "" {
			f["error"] = flashMsg
		}
	}
	fb, _ := json.Marshal(f)
	u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
	ev := model.LogEvent{
		Component: "portal",
		Actor:     u.Username,
		User:      u.Username,
		Action:    action,
		Fields:    string(fb),
	}
	if id > 0 {
		ev.Target = kind + ":" + strconv.FormatInt(int64(id), 10)
		if kind == "machine" {
			ev.MachineID = id
		}
	}
	if failed {
		ev.Level = "WARN"
		ev.Outcome = "failed"
		why := flashMsg
		if why == "" && status == http.StatusUnprocessableEntity {
			why = "the form had errors" // re-rendered with inline errors
		}
		ev.Message = auditFailureMessage(u.Username, kind, verb, name, why)
	} else {
		ev.Outcome = "ok"
	}
	// A fresh lookup: the request's own may have cached a table before a
	// create added the row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 2*time.Second)
	defer cancel()
	_ = r.Logs.Record(ctx, ev, model.NewNameLookup(ctx, r.Inventory))
}

// auditFailureMessage is the sentence for a change that didn't save, e.g.
// `admin tried to update image "Lab" — not saved: name is required`.
func auditFailureMessage(user, kind, verb, name, why string) string {
	base, ok := auditVerbBase[verb]
	if !ok {
		base = strings.NewReplacer(".", " ", "_", " ").Replace(verb)
	}
	noun := model.AuditNouns[kind]
	switch kind {
	case "settings":
		noun = "settings"
	case "machine":
		noun = "machine"
	}
	if noun == "" {
		noun = strings.ReplaceAll(kind, "_", " ")
	}
	msg := fmt.Sprintf("%s tried to %s %s", user, base, noun)
	if name != "" {
		msg += fmt.Sprintf(" “%s”", name)
	}
	if verb == "delete" || verb == "bulk_delete" {
		msg += " — not deleted"
	} else {
		msg += " — not saved"
	}
	if why != "" {
		msg += ": " + why
	}
	return msg
}

// flashFromHeader reads the flash cookie a handler set on this response.
func flashFromHeader(h http.Header) (kind, msg string) {
	for _, c := range h.Values("Set-Cookie") {
		if !strings.HasPrefix(c, flashCookieName+"=") {
			continue
		}
		v := strings.TrimPrefix(c, flashCookieName+"=")
		if i := strings.IndexByte(v, ';'); i >= 0 {
			v = v[:i]
		}
		b, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil || len(b) == 0 {
			continue
		}
		kind, msg, _ = strings.Cut(string(b), "|")
	}
	return kind, msg
}

// recordAuth writes a sign-in / sign-out audit row.
func recordAuth(req *http.Request, r Repos, action, user string, fields map[string]any) {
	if r.Logs == nil {
		return
	}
	fb, _ := json.Marshal(fields)
	ev := model.LogEvent{Component: "portal", Actor: user, User: user, Action: action, Fields: string(fb)}
	if action == "auth.login.fail" {
		ev.User = "" // an attempted username is not a real user
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 2*time.Second)
	defer cancel()
	_ = r.Logs.Record(ctx, ev, nil)
}

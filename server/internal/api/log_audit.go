package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// This file writes the server's own log_event rows: sign-ins through the
// JSON API, secret hand-outs and deployment milestones. Like notification
// emission, recording is best-effort — a handler never fails because its
// log row couldn't be written.

// recordEvent stores ev with its catalogue message rendered now.
func recordEvent(req *http.Request, r Repos, ev model.LogEvent) {
	if r.Logs == nil {
		return
	}
	if ev.Component == "" {
		ev.Component = "api"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 2*time.Second)
	defer cancel()
	var names *model.NameLookup
	if r.Inventory != nil {
		names = model.NewNameLookup(ctx, r.Inventory)
	}
	_ = r.Logs.Record(ctx, ev, names)
}

func fieldsJSON(f map[string]any) string {
	if len(f) == 0 {
		return "{}"
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// recordAPIAuth writes auth.login.ok / auth.login.fail / auth.logout for the
// JSON API's sign-in endpoints (the portal form records its own).
func recordAPIAuth(req *http.Request, r Repos, action, username, ip string) {
	f := map[string]any{"via": "api"}
	if ip != "" {
		f["ip"] = ip
	}
	ev := model.LogEvent{Actor: username, User: username, Action: action}
	if action == "auth.login.fail" {
		f["username"] = username
		ev.User = ""
	}
	ev.Fields = fieldsJSON(f)
	recordEvent(req, r, ev)
}

// reimageSourceLabel is the plain-English form of a re-image trigger token.
func reimageSourceLabel(source string) string {
	switch source {
	case model.ReimageSourceRemoteFlag:
		return "remote request from the portal"
	case model.ReimageSourceBootMenu:
		return "chosen at the boot menu"
	}
	return strings.ReplaceAll(source, "_", " ")
}

// recordDeployEvent writes a deployment milestone (deploy.started,
// deploy.reimage, deploy.staged, deploy.setup, deploy.finished,
// deploy.failed) tagged with the machine and the run, so it shows on the
// machine's timeline and under /portal/logs?deployment=N.
func recordDeployEvent(req *http.Request, r Repos, m model.MachineRecord, depID model.ID, imageID *model.ID, action string, f map[string]any) {
	if f == nil {
		f = map[string]any{}
	}
	if imageID != nil && *imageID > 0 && r.Images != nil {
		if im, err := r.Images.Get(req.Context(), *imageID); err == nil {
			f["image"] = im.Name
		}
	}
	if depID > 0 {
		f["deployment_id"] = int64(depID)
	}
	recordEvent(req, r, model.LogEvent{
		Component:    "api",
		Actor:        m.SystemUUID,
		Action:       action,
		MachineID:    m.ID,
		DeploymentID: depID,
		Fields:       fieldsJSON(f),
	})
}

// deployFailureSummary returns the first error logged during a deployment
// (its catalogue message and hint), for the deploy-failed notification.
// ok is false when the run logged no error (yet).
func deployFailureSummary(ctx context.Context, r Repos, depID model.ID) (msg, hint string, ok bool) {
	if r.Logs == nil || depID == 0 {
		return "", "", false
	}
	evs, err := r.Logs.Search(ctx, model.LogSearch{DeploymentID: depID, Levels: []string{"ERROR"}, ByID: true, Limit: 50})
	if err != nil || len(evs) == 0 {
		return "", "", false
	}
	first := evs[len(evs)-1] // newest first; the first error is the cause
	if first.Action == "deploy.failed" && len(evs) > 1 {
		first = evs[len(evs)-2]
	}
	if first.Action == "deploy.failed" {
		return "", "", false
	}
	model.ApplyLogDescription(&first, model.LogNames{})
	return first.Message, first.Hint, first.Message != ""
}

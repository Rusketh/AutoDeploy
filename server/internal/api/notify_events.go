package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// This file holds the API handlers' notification emission helpers. Emission
// is always best-effort and asynchronous (the emitter runs its own worker
// pool); a handler never fails because a notification couldn't be produced.

// machineDisplayName resolves the friendliest label we have for a machine:
// its desired binding name, then the agent-reported Windows name, then the
// record id.
func machineDisplayName(ctx context.Context, r Repos, m model.MachineRecord) string {
	if r.Inventory != nil {
		if b, err := r.Inventory.GetBinding(ctx, m.ID); err == nil && b.MachineName != "" {
			return b.MachineName
		}
	}
	if m.ReportedName != "" {
		return m.ReportedName
	}
	return fmt.Sprintf("machine #%d", int64(m.ID))
}

func machineLink(id model.ID) string {
	return fmt.Sprintf("/portal/machines/%d", int64(id))
}

// emitFirstSeen fires machine.first_seen when an upsert just created the
// machine record. Call after any UpsertFromIdentity whose result is kept.
func emitFirstSeen(req *http.Request, r Repos, m model.MachineRecord) {
	if !m.FirstContact {
		return
	}
	label := m.SystemUUID
	if m.SystemManufacturer != "" || m.SystemProduct != "" {
		label = fmt.Sprintf("%s %s (%s)", m.SystemManufacturer, m.SystemProduct, m.SystemUUID)
	}
	r.Emitter.Emit(req.Context(), notify.Event{
		Name:     notify.EventMachineFirstSeen,
		Severity: notify.SevInfo,
		Title:    "New machine seen: " + label,
		Body:     "A machine reported for the first time and was added to inventory.",
		Link:     machineLink(m.ID),
		Actor:    "system",
	})
}

// emitDeployStarted fires when the boot client begins staging a deploy:
// deploy.reimage when this rebuilds an existing machine (reimageSource
// non-empty, from reimageSourceFor), deploy.started for a first install.
func emitDeployStarted(req *http.Request, r Repos, m model.MachineRecord, imageID *model.ID, reimageSource string) {
	if r.Emitter == nil {
		return
	}
	name := machineDisplayName(req.Context(), r, m)
	image := ""
	if imageID != nil && r.Images != nil {
		if im, err := r.Images.Get(req.Context(), *imageID); err == nil {
			image = im.Name
		}
	}
	ev := notify.Event{
		Name:     notify.EventDeployStarted,
		Severity: notify.SevInfo,
		Title:    "Deploy started on " + name,
		Link:     machineLink(m.ID),
		Actor:    "system",
	}
	if image != "" {
		ev.Body = "Image: " + image + "."
	}
	if reimageSource != "" {
		ev.Name = notify.EventDeployReimage
		ev.Severity = notify.SevWarning
		ev.Title = "Re-image started on " + name
		ev.Body = strings.TrimSpace("The machine is being wiped and rebuilt (trigger: " + reimageSource + "). " + ev.Body)
	}
	r.Emitter.Emit(req.Context(), ev)
}

// emitDeployOutcome fires deploy.completed / deploy.failed when a deploy
// reaches a terminal outcome (boot-client staging failure or the agent's
// final report).
func emitDeployOutcome(req *http.Request, r Repos, m model.MachineRecord, outcome, notes string) {
	if r.Emitter == nil {
		return
	}
	name := machineDisplayName(req.Context(), r, m)
	ev := notify.Event{
		Name:     notify.EventDeployCompleted,
		Severity: notify.SevSuccess,
		Title:    "Deploy completed on " + name,
		Body:     notes,
		Link:     machineLink(m.ID),
		Actor:    "system",
	}
	if outcome == "failed" {
		ev.Name = notify.EventDeployFailed
		ev.Severity = notify.SevError
		ev.Title = "Deploy failed on " + name
	}
	r.Emitter.Emit(req.Context(), ev)
}

// emitLoginFailed fires auth.login_failed for a rejected portal/API sign-in.
// Carries the attempted username and source IP — never anything from the
// password field (CONVENTIONS.md §4).
func emitLoginFailed(req *http.Request, r Repos, username, ip string) {
	if r.Emitter == nil {
		return
	}
	if username == "" {
		username = "(blank)"
	}
	r.Emitter.Emit(req.Context(), notify.Event{
		Name:     notify.EventAuthLoginFailed,
		Severity: notify.SevWarning,
		Title:    "Failed login attempt for " + username,
		Body:     "A sign-in with this username was rejected (source IP " + ip + ").",
		Link:     "/portal/logs",
		Actor:    username,
	})
}

// emitADJoinFailure fires ad.join_failed / ad.trust_failed for a machine whose
// AD join status just transitioned into a failure. event is the model status
// token (model.ADJoinFailed or model.ADTrustBroken); detail is the agent's
// reported error text (never a credential). Best-effort and deduped upstream —
// the caller only invokes this on a genuine transition, so it fires once per
// failure episode.
func emitADJoinFailure(req *http.Request, r Repos, m model.MachineRecord, event, detail string) {
	if r.Emitter == nil {
		return
	}
	name := machineDisplayName(req.Context(), r, m)
	ev := notify.Event{
		Severity: notify.SevError,
		Link:     machineLink(m.ID),
		Actor:    "system",
	}
	switch event {
	case model.ADTrustBroken:
		ev.Name = notify.EventADTrustFailed
		ev.Title = "AD trust broken on " + name
		ev.Body = "The trust relationship between this machine and the domain has failed. It likely needs to be rejoined."
	default:
		ev.Name = notify.EventADJoinFailed
		ev.Title = "AD join failed on " + name
		ev.Body = "The agent could not join this machine to Active Directory."
	}
	if detail != "" {
		ev.Body += " Detail: " + detail
	}
	r.Emitter.Emit(req.Context(), ev)
}

// emitBulkFinished loads the operation + latest-run rollup and emits the
// appropriate bulk.completed/failed/partial event. opID 0 is a no-op, so
// callers can pass CompleteJob's return straight through.
func emitBulkFinished(req *http.Request, r Repos, opID model.ID) {
	if opID == 0 || r.Emitter == nil {
		return
	}
	op, _, err := r.Bulk.GetOperation(req.Context(), opID)
	if err != nil {
		return
	}
	prog, err := r.Bulk.ProgressFor(req.Context(), opID)
	if err != nil {
		return
	}
	r.Emitter.Emit(req.Context(), notify.BulkOutcomeEvent(op, prog))
}

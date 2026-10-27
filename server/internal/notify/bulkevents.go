package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// BulkCreatedEvent describes a freshly created bulk operation. Shared by the
// API and portal create paths so both produce identical notifications.
func BulkCreatedEvent(op model.BulkOperation) Event {
	return Event{
		Name:     EventBulkCreated,
		Severity: SevInfo,
		Title:    "Bulk operation created: " + op.Name,
		Body:     op.Description,
		Link:     fmt.Sprintf("/portal/bulk/%d", int64(op.ID)),
		Actor:    op.CreatedBy,
	}
}

// UpdateDeployedEvent describes a freshly created Windows Update deployment.
// Shared by the API and portal create paths. kbs are the deployed updates' KB
// numbers (in UpdateIDs order); when given they lead the title so the
// operator sees what was pushed, not just a count.
func UpdateDeployedEvent(dep model.UpdateDeployment, jobs int, kbs ...string) Event {
	title := fmt.Sprintf("Update deployment created (%d update(s), %d machine job(s))",
		len(dep.UpdateIDs), jobs)
	var fields map[string]any
	if len(kbs) > 0 {
		fields = map[string]any{"kbs": strings.Join(kbs, ",")}
		shown := kbs
		more := ""
		if len(shown) > 4 {
			more = fmt.Sprintf(" +%d more", len(shown)-4)
			shown = shown[:4]
		}
		title = fmt.Sprintf("Update deployment created: %s%s (%d machine job(s))",
			strings.Join(shown, ", "), more, jobs)
	}
	return Event{
		Name:     EventUpdateDeployed,
		Severity: SevInfo,
		Title:    title,
		Link:     fmt.Sprintf("/portal/update-deployments/%d", int64(dep.ID)),
		Actor:    dep.CreatedBy,
		Fields:   fields,
	}
}

// UpdateKBNumbers maps update IDs to their KB numbers for UpdateDeployedEvent,
// skipping IDs that no longer resolve. Shared by the API and portal paths.
func UpdateKBNumbers(ctx context.Context, repo *model.WindowsUpdateRepo, ids []model.ID) []string {
	if repo == nil || len(ids) == 0 {
		return nil
	}
	all, err := repo.List(ctx)
	if err != nil {
		return nil
	}
	byID := make(map[model.ID]string, len(all))
	for _, u := range all {
		byID[u.ID] = u.KBNumber
	}
	var out []string
	for _, id := range ids {
		if kb := byID[id]; kb != "" {
			out = append(out, kb)
		}
	}
	return out
}

// BulkOutcomeEvent classifies a finished bulk operation run from its job
// rollup: every job ok -> bulk.completed, every job failed -> bulk.failed,
// anything in between (failures or expiry-cancelled jobs alongside
// successes) -> bulk.partial. Shared by the agent job-result handler and
// the scheduler's expiry sweep — the two places an operation can finish.
func BulkOutcomeEvent(op model.BulkOperation, p model.BulkProgress) Event {
	name, sev := EventBulkCompleted, SevSuccess
	switch {
	case p.Total > 0 && p.OK == 0:
		name, sev = EventBulkFailed, SevError
	case p.Failed > 0 || p.Cancelled > 0:
		name, sev = EventBulkPartial, SevWarning
	}
	return Event{
		Name:     name,
		Severity: sev,
		Title:    fmt.Sprintf("Bulk operation finished: %s", op.Name),
		Body: fmt.Sprintf("%d of %d job(s) succeeded, %d failed, %d cancelled.",
			p.OK, p.Total, p.Failed, p.Cancelled),
		Link:  fmt.Sprintf("/portal/bulk/%d", int64(op.ID)),
		Actor: "system",
		Fields: map[string]any{
			"operation_id": int64(op.ID),
			"action":       op.Action,
			"ok":           p.OK,
			"failed":       p.Failed,
			"cancelled":    p.Cancelled,
			"total":        p.Total,
		},
	}
}

package notify

import (
	"fmt"

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
// Shared by the API and portal create paths.
func UpdateDeployedEvent(dep model.UpdateDeployment, jobs int) Event {
	return Event{
		Name:     EventUpdateDeployed,
		Severity: SevInfo,
		Title: fmt.Sprintf("Update deployment created (%d update(s), %d machine job(s))",
			len(dep.UpdateIDs), jobs),
		Link:  fmt.Sprintf("/portal/update-deployments/%d", int64(dep.ID)),
		Actor: dep.CreatedBy,
	}
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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// RegisterToolkit mounts the agent-facing Tool Kit endpoints.
//
// Both are authenticated by the server-minted agent_id (the same capability
// the /self loop and the domain-join endpoint use) AND bound to a specific
// job: the server only answers for a toolkit job that is claimed by (running
// on) the very machine asking. A queued, finished, cancelled or foreign job
// gets nothing -- in particular never a password.
func RegisterToolkit(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/agent/toolkit-job", handleAgentToolkitJob(r))
	mux.HandleFunc("POST /api/v1/agent/job-credential", handleAgentJobCredential(r))
}

type agentToolkitJobReq struct {
	AgentID string   `json:"agent_id"`
	JobID   model.ID `json:"job_id"`
}

// AgentToolkitJobResponse is everything the agent needs to run one toolkit
// job except the Run As password, which it fetches separately (and only when
// the effective mode is a specific account) from /agent/job-credential.
type AgentToolkitJobResponse struct {
	JobID          model.ID `json:"job_id"`
	ToolkitID      model.ID `json:"toolkit_id"`
	Name           string   `json:"name"`
	Revision       int      `json:"revision"`
	QueuedRevision int      `json:"queued_revision,omitempty"`
	// RunAs is the effective mode: system | user | logged_on_user.
	RunAs     string `json:"run_as"`
	RunAsUser string `json:"run_as_user,omitempty"`
	// NeedsPassword is true when RunAs is "user": fetch the credential.
	NeedsPassword  bool   `json:"needs_password"`
	TimeoutMinutes int    `json:"timeout_minutes"`
	RebootPolicy   string `json:"reboot_policy"`
	// Item reuses the software item shape (files, bundles, steps) so the
	// agent's download + step pipeline runs it unchanged. Steps arrive with
	// the kit's parameters already substituted.
	Item AgentSoftwareItem `json:"item"`
}

// AgentJobCredentialResponse carries a Run As password. Secret: returned
// only to the agent running the job, never logged.
type AgentJobCredentialResponse struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// toolkitJobContext authorises a request and loads the job, its machine,
// payload and kit. It writes the HTTP error itself and returns ok=false on
// any refusal.
type toolkitJobContext struct {
	machine model.MachineRecord
	job     model.BulkJob
	payload model.ToolkitPayload
	kit     model.Toolkit
}

func loadToolkitJob(w http.ResponseWriter, req *http.Request, r Repos) (toolkitJobContext, bool) {
	var c toolkitJobContext
	if r.Toolkits == nil || r.Bulk == nil || r.Inventory == nil {
		http.Error(w, "tool kits not configured", http.StatusNotFound)
		return c, false
	}
	var in agentToolkitJobReq
	if err := decodeJSON(req, &in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return c, false
	}
	agentID := strings.TrimSpace(in.AgentID)
	if agentID == "" || in.JobID <= 0 {
		http.Error(w, "agent_id and job_id required", http.StatusBadRequest)
		return c, false
	}
	m, err := r.Inventory.GetByAgentID(req.Context(), agentID)
	if err != nil {
		// Unknown agent_id: don't reveal whether the job exists.
		http.Error(w, "forbidden", http.StatusForbidden)
		return c, false
	}
	job, err := r.Bulk.GetJob(req.Context(), in.JobID)
	if err != nil || job.MachineID != m.ID || job.Action != model.BulkActionToolkit {
		http.Error(w, "forbidden", http.StatusForbidden)
		return c, false
	}
	if job.Status != "running" {
		// Only a claimed, in-flight job may fetch its definition/credential.
		http.Error(w, "job is not running", http.StatusConflict)
		return c, false
	}
	p, err := model.ParseToolkitPayload(job.Payload)
	if err != nil {
		writeError(w, err)
		return c, false
	}
	kit, err := r.Toolkits.Get(req.Context(), p.ToolkitID)
	if err != nil {
		writeError(w, err)
		return c, false
	}
	return toolkitJobContext{machine: m, job: job, payload: p, kit: kit}, true
}

// handleAgentToolkitJob returns a running toolkit job's definition: files,
// parameter-expanded steps, effective Run As mode, timeout and reboot policy.
func handleAgentToolkitJob(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		c, ok := loadToolkitJob(w, req, r)
		if !ok {
			return
		}
		steps, err := swspec.ParseSteps(c.kit.StepsJSON)
		if err != nil {
			writeError(w, fmt.Errorf("%w: %v", model.ErrValidation, err))
			return
		}
		values, err := model.ResolveParams(c.kit.Params, c.payload.Params)
		if err != nil {
			writeError(w, err)
			return
		}
		steps = model.ExpandToolkitSteps(steps, values)
		mode, user := model.EffectiveRunAs(c.kit, c.payload)
		base := "/payload/toolkit/"
		item := AgentSoftwareItem{
			PackageID:    c.kit.ID,
			Name:         c.kit.Name,
			Files:        listToolkitFiles(r, c.kit.ID, base),
			Bundles:      listToolkitBundles(r, c.kit.ID, base),
			InstallSteps: steps,
		}
		if item.InstallSteps == nil {
			item.InstallSteps = []swspec.InstallStep{}
		}
		writeJSON(w, http.StatusOK, AgentToolkitJobResponse{
			JobID:          c.job.ID,
			ToolkitID:      c.kit.ID,
			Name:           c.kit.Name,
			Revision:       c.kit.Revision,
			QueuedRevision: c.payload.Revision,
			RunAs:          mode,
			RunAsUser:      user,
			NeedsPassword:  mode == model.RunAsUser,
			TimeoutMinutes: c.kit.TimeoutMinutes,
			RebootPolicy:   c.kit.RebootPolicy,
			Item:           item,
		})
	}
}

// handleAgentJobCredential releases the Run As password for a running
// toolkit job whose effective mode is a specific account. The password is
// the operation's deploy-time override when one was entered, otherwise the
// kit's stored password (only when the account is the kit's own). Every
// hand-out is audit-logged; the value never is.
func handleAgentJobCredential(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		c, ok := loadToolkitJob(w, req, r)
		if !ok {
			return
		}
		mode, user := model.EffectiveRunAs(c.kit, c.payload)
		if mode != model.RunAsUser {
			http.Error(w, "this job does not run as a specific account", http.StatusConflict)
			return
		}
		pw, source, err := toolkitJobPassword(req.Context(), r, c, user)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				http.Error(w, "no password is stored for the run-as account "+user, http.StatusNotFound)
				return
			}
			http.Error(w, "credential unavailable", http.StatusInternalServerError)
			return
		}
		recordEvent(req, r, model.LogEvent{
			Actor:     c.machine.SystemUUID,
			Action:    "secret.access",
			Target:    fmt.Sprintf("toolkit:%d", int64(c.kit.ID)),
			MachineID: c.machine.ID,
			Fields: fieldsJSON(map[string]any{
				"secret": "tool kit run-as password", "by": "agent",
				"account": user, "source": source,
				"job_id": int64(c.job.ID), "operation_id": int64(c.job.OperationID),
				"note": "value not logged",
			}),
		})
		writeJSON(w, http.StatusOK, AgentJobCredentialResponse{User: user, Password: pw})
	}
}

// toolkitJobPassword picks the password for a job's Run As account: the
// operation override first, then the kit's own password when the account is
// the kit's configured one. source says which ("operation" or "toolkit").
func toolkitJobPassword(ctx context.Context, r Repos, c toolkitJobContext, user string) (pw, source string, err error) {
	ouser, opw, oerr := r.Toolkits.RetrieveOperationCredential(ctx, c.job.OperationID)
	switch {
	case oerr == nil && strings.EqualFold(ouser, user):
		return opw, "operation", nil
	case oerr != nil && !errors.Is(oerr, model.ErrNotFound):
		return "", "", oerr
	}
	if c.kit.RunAsMode == model.RunAsUser && strings.EqualFold(c.kit.RunAsUser, user) && c.kit.HasPassword {
		pw, err := r.Toolkits.RetrievePassword(ctx, c.kit.ID)
		if err != nil {
			return "", "", err
		}
		return pw, "toolkit", nil
	}
	return "", "", fmt.Errorf("run-as password for %s: %w", user, model.ErrNotFound)
}

// listToolkitFiles / listToolkitBundles advertise a kit's files and bundle
// zips, exactly like a software package's.
func listToolkitFiles(r Repos, id model.ID, base string) []AgentPackageFile {
	if r.Blobs == nil {
		return nil
	}
	entries, err := r.Blobs.ListTree("toolkit/" + idStr(id) + "/files")
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make([]AgentPackageFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, AgentPackageFile{
			Name: e.Name, URL: base + idStr(id) + "/files/" + e.Name, SizeBytes: e.Size,
		})
	}
	return out
}

func listToolkitBundles(r Repos, id model.ID, base string) []AgentPackageFile {
	if r.Blobs == nil {
		return nil
	}
	entries, err := r.Blobs.ListDir("toolkit/" + idStr(id) + "/bundle")
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make([]AgentPackageFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, AgentPackageFile{
			Name: e.Name, URL: base + idStr(id) + "/bundle/" + e.Name, SizeBytes: e.Size,
		})
	}
	return out
}

// emitToolkitResult fires toolkit.run_ok/run_failed for a finished toolkit
// job, naming the kit and machine.
func emitToolkitResult(req *http.Request, r Repos, jobID model.ID, status string) {
	if r.Emitter == nil {
		return
	}
	job, err := r.Bulk.GetJob(req.Context(), jobID)
	if err != nil || job.Action != model.BulkActionToolkit {
		return
	}
	kitName := "tool kit"
	if p, perr := model.ParseToolkitPayload(job.Payload); perr == nil && r.Toolkits != nil {
		if k, kerr := r.Toolkits.Get(req.Context(), p.ToolkitID); kerr == nil {
			kitName = k.Name
		}
	}
	machine := fmt.Sprintf("machine #%d", int64(job.MachineID))
	if r.Inventory != nil {
		if m, merr := r.Inventory.Get(req.Context(), job.MachineID); merr == nil {
			machine = machineDisplayName(req.Context(), r, m)
		}
	}
	ev := notify.Event{
		Name:     notify.EventToolkitRunOK,
		Severity: notify.SevSuccess,
		Title:    fmt.Sprintf("Ran %s on %s", kitName, machine),
		Link:     fmt.Sprintf("/portal/bulk/%d", int64(job.OperationID)),
		Actor:    "system",
	}
	if status == "failed" {
		ev.Name = notify.EventToolkitRunFailed
		ev.Severity = notify.SevError
		ev.Title = fmt.Sprintf("%s failed on %s", kitName, machine)
		ev.Body = "Open the operation to see each step's exit code and output."
	}
	r.Emitter.Emit(req.Context(), ev)
}

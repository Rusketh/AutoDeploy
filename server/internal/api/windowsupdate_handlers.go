package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// AgentUpdateJob is one claimed update-deployment job enriched with the
// update metadata the agent needs to download, install, and finalize it:
// the payload's real filename (drives the wusa-vs-dism choice by
// extension), the KB number, and the operator's reboot_after flag. The
// extra fields are additive — old agents ignore them.
type AgentUpdateJob struct {
	model.UpdateDeploymentJob
	KBNumber        string `json:"kb_number,omitempty"`
	PayloadFilename string `json:"payload_filename,omitempty"`
	RebootAfter     bool   `json:"reboot_after,omitempty"`
	SizeBytes       int64  `json:"size_bytes,omitempty"`
}

// RegisterWindowsUpdates mounts the /api/v1/updates/* routes.
func RegisterWindowsUpdates(mux *http.ServeMux, r Repos) {
	if r.Updates == nil {
		return
	}

	mux.HandleFunc("GET /api/v1/updates", requireAuth(r, handleListUpdates(r)))
	mux.HandleFunc("POST /api/v1/updates", requireAuth(r, handleCreateUpdate(r)))
	mux.HandleFunc("GET /api/v1/updates/{id}", requireAuth(r, handleGetUpdate(r)))
	mux.HandleFunc("PUT /api/v1/updates/{id}", requireAuth(r, handleUpdateUpdate(r)))
	mux.HandleFunc("DELETE /api/v1/updates/{id}", requireAuth(r, handleDeleteUpdate(r)))
	mux.HandleFunc("GET /api/v1/updates/{id}/compliance", requireAuth(r, handleUpdateCompliance(r)))
	mux.HandleFunc("POST /api/v1/update-deployments", requireAuth(r, handleCreateUpdateDeployment(r)))
	mux.HandleFunc("GET /api/v1/update-deployments", requireAuth(r, handleListUpdateDeployments(r)))
	mux.HandleFunc("GET /api/v1/update-deployments/{id}", requireAuth(r, handleGetUpdateDeployment(r)))
	mux.HandleFunc("GET /api/v1/machines/{id}/updates", requireAuth(r, handleMachineUpdates(r)))

	// Agent endpoints (unauthenticated — called by the agent).
	mux.HandleFunc("POST /api/v1/agent/update-status", handleAgentUpdateStatus(r))
	mux.HandleFunc("POST /api/v1/agent/update-job/{id}/result", handleAgentUpdateJobResult(r))
}

func handleListUpdates(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		list, err := r.Updates.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func handleCreateUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.WindowsUpdate
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		u, err := r.Updates.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, u)
	}
}

func handleGetUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

func handleUpdateUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		var in model.WindowsUpdate
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		in.ID = id
		if err := r.Updates.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, in)
	}
}

func handleDeleteUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		if err := r.Updates.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

func handleUpdateCompliance(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		s, err := r.Updates.ComplianceForUpdate(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

func handleCreateUpdateDeployment(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			UpdateIDs []model.ID       `json:"update_ids"`
			Target    model.BulkTarget `json:"target"`
			OSFilter  string           `json:"os_filter"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		user, _ := UserFromRequest(req, r)
		d := model.UpdateDeployment{
			UpdateIDs: in.UpdateIDs,
			Target:    in.Target,
			OSFilter:  in.OSFilter,
			CreatedBy: user.Username,
		}
		dep, jobs, err := r.Updates.CreateDeployment(req.Context(), d)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"deployment": dep,
			"jobs":       jobs,
		})
	}
}

func handleListUpdateDeployments(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		list, err := r.Updates.ListDeployments(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func handleGetUpdateDeployment(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		dep, jobs, err := r.Updates.GetDeployment(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deployment": dep,
			"jobs":       jobs,
		})
	}
}

func handleMachineUpdates(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		statuses, err := r.Updates.ListMachineStatuses(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if statuses == nil {
			statuses = []model.MachineUpdateStatus{}
		}
		writeJSON(w, http.StatusOK, statuses)
	}
}

// handleAgentUpdateStatus accepts a KB scan report from the agent.
func handleAgentUpdateStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			AgentID   string   `json:"agent_id"`
			KBNumbers []string `json:"kb_numbers"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		m, err := r.Inventory.GetByAgentID(req.Context(), in.AgentID)
		if err != nil {
			writeError(w, err)
			return
		}
		statuses := make([]model.MachineUpdateStatus, len(in.KBNumbers))
		for i, kb := range in.KBNumbers {
			statuses[i] = model.MachineUpdateStatus{
				MachineID: m.ID,
				KBNumber:  kb,
				Status:    "installed",
			}
		}
		if err := r.Updates.UpsertMachineStatuses(req.Context(), m.ID, statuses); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleAgentUpdateJobResult accepts the result of a single update job.
func handleAgentUpdateJobResult(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := req.PathValue("id")
		jobID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid job id"})
			return
		}
		var in struct {
			Status     string `json:"status"`
			ResultJSON string `json:"result_json"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		if err := r.Updates.CompleteUpdateJob(req.Context(), model.ID(jobID), in.Status, in.ResultJSON); err != nil {
			writeError(w, err)
			return
		}
		// Reflect the result in per-machine compliance immediately rather
		// than waiting for the agent's next periodic KB scan. Best-effort:
		// the job completion above already succeeded.
		if job, jerr := r.Updates.GetUpdateJob(req.Context(), model.ID(jobID)); jerr == nil {
			if u, uerr := r.Updates.Get(req.Context(), job.UpdateID); uerr == nil {
				st := model.MachineUpdateStatus{
					MachineID: job.MachineID,
					KBNumber:  u.KBNumber,
					Status:    "failed",
				}
				if in.Status == "ok" {
					now := time.Now()
					st.Status, st.InstalledAt = "installed", &now
				}
				_ = r.Updates.UpsertMachineStatuses(req.Context(), job.MachineID,
					[]model.MachineUpdateStatus{st})
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

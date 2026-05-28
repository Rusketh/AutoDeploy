package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterBulk mounts the bulk-operations and agent-checkin endpoints.
func RegisterBulk(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/bulk/preview", handleBulkPreview(r))
	mux.HandleFunc("POST /api/v1/bulk/operations", handleBulkCreate(r))
	mux.HandleFunc("GET /api/v1/bulk/operations", handleBulkList(r))
	mux.HandleFunc("GET /api/v1/bulk/operations/{id}", handleBulkGet(r))

	// Agent check-in.
	mux.HandleFunc("POST /api/v1/agent/checkin", handleAgentCheckin(r))
	mux.HandleFunc("POST /api/v1/agent/jobs/{id}/result", handleAgentJobResult(r))
}

func handleBulkPreview(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var t model.BulkTarget
		if err := decodeJSON(req, &t); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		machines, err := r.Bulk.PreviewTargets(req.Context(), t)
		if err != nil {
			writeError(w, err)
			return
		}
		if machines == nil {
			machines = []model.MachineRecord{}
		}
		writeJSON(w, http.StatusOK, machines)
	}
}

func handleBulkCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromRequest(req, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in model.BulkOperation
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		in.CreatedBy = u.Username
		op, jobs, err := r.Bulk.CreateOperation(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"operation": op,
			"jobs":      jobs,
		})
	}
}

func handleBulkList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ops, err := r.Bulk.ListOperations(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if ops == nil {
			ops = []model.BulkOperation{}
		}
		writeJSON(w, http.StatusOK, ops)
	}
}

func handleBulkGet(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		op, jobs, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"operation": op, "jobs": jobs})
	}
}

type checkinReq struct {
	Identity match.Identity `json:"identity"`
}

func handleAgentCheckin(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in checkinReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		jobs, err := r.Bulk.ClaimJobsFor(req.Context(), m.ID, 8)
		if err != nil {
			writeError(w, err)
			return
		}
		if jobs == nil {
			jobs = []model.BulkJob{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"machine_id": m.ID,
			"jobs":       jobs,
		})
	}
}

type jobResultReq struct {
	Status string `json:"status"`
	Result string `json:"result_json,omitempty"`
}

func handleAgentJobResult(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in jobResultReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := r.Bulk.CompleteJob(req.Context(), id, in.Status, in.Result); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

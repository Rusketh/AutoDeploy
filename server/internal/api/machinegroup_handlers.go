package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListGroups(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Groups.ListWithCounts(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.MachineGroup{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateGroup(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.MachineGroup
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Groups.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetGroup(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Groups.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateGroup(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.MachineGroup
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Groups.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Groups.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteGroup(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Groups.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// groupMembersBody is the request body for the manual-membership endpoints and
// the response for the resolved-members read.
type groupMembersBody struct {
	MachineIDs []model.ID `json:"machine_ids"`
}

func handleListGroupMembers(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		ids, err := r.Groups.Members(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if ids == nil {
			ids = []model.ID{}
		}
		writeJSON(w, http.StatusOK, groupMembersBody{MachineIDs: ids})
	}
}

func handleAddGroupMembers(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in groupMembersBody
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Groups.AddMembers(req.Context(), id, in.MachineIDs); err != nil {
			writeError(w, err)
			return
		}
		handleListGroupMembers(r)(w, req)
	}
}

func handleRemoveGroupMembers(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in groupMembersBody
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Groups.RemoveMembers(req.Context(), id, in.MachineIDs); err != nil {
			writeError(w, err)
			return
		}
		handleListGroupMembers(r)(w, req)
	}
}

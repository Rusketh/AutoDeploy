package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListLoadouts(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Loadouts.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.SoftwareLoadout{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateLoadout(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.SoftwareLoadout
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Loadouts.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetLoadout(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Loadouts.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateLoadout(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.SoftwareLoadout
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := r.Loadouts.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Loadouts.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteLoadout(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Loadouts.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Software.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.SoftwarePackage{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.SoftwarePackage
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Software.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Software.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.SoftwarePackage
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Software.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Software.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Software.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

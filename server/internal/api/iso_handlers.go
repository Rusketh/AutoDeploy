package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListISOs(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		isos, err := r.ISOs.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if isos == nil {
			isos = []model.ISO{}
		}
		writeJSON(w, http.StatusOK, isos)
	}
}

func handleCreateISO(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.ISO
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.ISOs.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetISO(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.ISOs.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateISO(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.ISO
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		if err := r.ISOs.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.ISOs.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteISO(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.ISOs.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

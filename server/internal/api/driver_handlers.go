package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListDrivers(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Drivers.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.DriverPackage{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateDriver(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.DriverPackage
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Drivers.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetDriver(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateDriver(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.DriverPackage
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := r.Drivers.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Drivers.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteDriver(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Drivers.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

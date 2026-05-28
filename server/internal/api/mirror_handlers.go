package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterMirrors mounts the payload-mirror admin endpoints.
func RegisterMirrors(mux *http.ServeMux, r Repos) {
	if r.Mirrors == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/mirrors", handleListMirrors(r))
	mux.HandleFunc("POST /api/v1/mirrors", handleCreateMirror(r))
	mux.HandleFunc("GET /api/v1/mirrors/{id}", handleGetMirror(r))
	mux.HandleFunc("PUT /api/v1/mirrors/{id}", handleUpdateMirror(r))
	mux.HandleFunc("DELETE /api/v1/mirrors/{id}", handleDeleteMirror(r))
}

func handleListMirrors(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		v, err := r.Mirrors.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.PayloadMirror{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateMirror(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in model.PayloadMirror
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Mirrors.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetMirror(r Repos) http.HandlerFunc {
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
		v, err := r.Mirrors.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateMirror(r Repos) http.HandlerFunc {
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
		var in model.PayloadMirror
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := r.Mirrors.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Mirrors.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteMirror(r Repos) http.HandlerFunc {
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
		if err := r.Mirrors.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

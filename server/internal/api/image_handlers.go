package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListImages(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Images.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.Image{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.Image
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := r.Images.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Images.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.Image
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := r.Images.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Images.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Images.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleResolveImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Resolver.Resolve(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

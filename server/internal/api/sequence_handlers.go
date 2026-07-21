package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func handleListSequences(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Sequences.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if v == nil {
			v = []model.Sequence{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleCreateSequence(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in model.Sequence
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		// The JSON API authors shared library sequences only; image-owned
		// inline sequences are created via the portal image form.
		in.OwnerImageID = nil
		out, err := r.Sequences.Create(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}

func handleGetSequence(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Sequences.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleUpdateSequence(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.Sequence
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.ID = id
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Sequences.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Sequences.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

func handleDeleteSequence(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Sequences.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

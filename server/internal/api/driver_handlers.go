package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/match"
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
		if err := validateName(in.Name); err != nil {
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
		if err := validateName(in.Name); err != nil {
			writeError(w, err)
			return
		}
		if err := r.Drivers.Update(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		v, _ := r.Drivers.Get(req.Context(), id)
		writeJSON(w, http.StatusOK, v)
	}
}

// handleDriverPreview answers "would this package's filters match this
// hardware?". POST body is a match.Identity; response carries a per-filter
// breakdown plus the overall verdict.
func handleDriverPreview(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		pkg, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		var identity match.Identity
		if err := decodeJSON(req, &identity); err != nil {
			writeError(w, err)
			return
		}
		type filterResult struct {
			FilterID model.ID `json:"filter_id"`
			JSON     string   `json:"filter_json"`
			Matches  bool     `json:"matches"`
			Error    string   `json:"error,omitempty"`
		}
		out := struct {
			DriverID       model.ID       `json:"driver_id"`
			Name           string         `json:"name"`
			Identity       match.Identity `json:"identity"`
			Filters        []filterResult `json:"filters"`
			PackageMatches bool           `json:"package_matches"`
		}{DriverID: pkg.ID, Name: pkg.Name, Identity: identity}
		for _, f := range pkg.Filters {
			fr := filterResult{FilterID: f.ID, JSON: f.FilterJSON}
			parsed, perr := match.ParseFilter(f.FilterJSON)
			if perr != nil {
				fr.Error = perr.Error()
				out.Filters = append(out.Filters, fr)
				continue
			}
			fr.Matches = parsed.Matches(identity)
			out.Filters = append(out.Filters, fr)
			if fr.Matches {
				out.PackageMatches = true
			}
		}
		writeJSON(w, http.StatusOK, out)
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

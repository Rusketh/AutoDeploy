package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/wingetsrc"
)

// RegisterWinget mounts the operator-driven "import an app from winget as
// an offline package" routes: search, manifest lookup (for the arch/scope
// chooser), import kickoff, and import progress. Everything here runs on
// explicit operator action — nothing downloads automatically.
func RegisterWinget(mux *http.ServeMux, r Repos) {
	if r.Winget == nil || r.Software == nil || r.Blobs == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/winget/search", requireAuth(r, handleWingetSearch(r)))
	mux.HandleFunc("GET /api/v1/winget/manifest", requireAuth(r, handleWingetManifest(r)))
	mux.HandleFunc("POST /api/v1/software/import", requireAuth(r, handleSoftwareImport(r)))
	mux.HandleFunc("GET /api/v1/software/{id}/import-status", requireAuth(r, handleSoftwareImportStatus(r)))
}

func handleWingetSearch(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		if q == "" || len(q) > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "q query parameter required (max 200 chars)"})
			return
		}
		results, err := r.Winget.Search(req.Context(), q)
		if err != nil {
			// Upstream (winget source) failure, not ours.
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		if results == nil {
			results = []wingetsrc.Result{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

// handleWingetManifest resolves a package's manifest and returns its
// installer variants so the portal can offer an arch/scope override before
// import.
func handleWingetManifest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimSpace(req.URL.Query().Get("id"))
		if id == "" || len(id) > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "id query parameter required"})
			return
		}
		version := strings.TrimSpace(req.URL.Query().Get("version"))
		m, err := r.Winget.Manifest(req.Context(), id, version)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}

// handleSoftwareImport resolves a winget package, builds a standard
// SoftwarePackage (detection + ordered steps), creates the row, and starts
// the background installer download. The result is identical to a package
// an operator built and uploaded by hand — fully editable afterwards.
func handleSoftwareImport(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			PackageIdentifier string `json:"package_identifier"`
			Version           string `json:"version"`
			Architecture      string `json:"architecture"`
			Scope             string `json:"scope"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		pkgID := strings.TrimSpace(in.PackageIdentifier)
		if pkgID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "package_identifier required"})
			return
		}

		ctx := req.Context()
		m, err := r.Winget.Manifest(ctx, pkgID, strings.TrimSpace(in.Version))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		chosen := wingetsrc.PickInstaller(m.Installers, in.Architecture, in.Scope)
		if chosen == nil {
			writeJSON(w, http.StatusUnprocessableEntity,
				apiError{Error: "this package has no importable installer"})
			return
		}
		if strings.TrimSpace(chosen.URL) == "" {
			writeJSON(w, http.StatusUnprocessableEntity,
				apiError{Error: "the chosen installer has no download URL"})
			return
		}

		// Resolve direct prerequisites (one level). A prereq that can't be
		// resolved is noted in the package description, not fatal.
		deps, unresolved := resolveWingetDeps(ctx, r.Winget, m.Dependencies, in.Architecture, in.Scope)

		built, err := wingetsrc.BuildPackage(m, chosen, deps, unresolved)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error()})
			return
		}

		detJSON, _ := json.Marshal(built.Detection)
		stepsJSON, _ := json.Marshal(built.Steps)
		pkg, err := r.Software.Create(ctx, model.SoftwarePackage{
			Name:          built.Name,
			Description:   built.Description,
			DetectionJSON: string(detJSON),
			StepsJSON:     string(stepsJSON),
		})
		if errors.Is(err, model.ErrConflict) {
			writeJSON(w, http.StatusConflict,
				apiError{Error: "a software package named " + built.Name + " already exists; rename or delete it first"})
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}

		files := make([]payload.SoftwareDownloadFile, 0, len(built.Files))
		for _, f := range built.Files {
			files = append(files, payload.SoftwareDownloadFile{
				URL: f.URL, Filename: f.Filename, SHA256: f.SHA256,
			})
		}
		payload.StartSoftwareImportAsync(r.Blobs, pkg.ID, files)

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":      pkg.ID,
			"name":    pkg.Name,
			"version": m.Version,
		})
	}
}

// resolveWingetDeps resolves each declared prerequisite identifier to a
// manifest + installer using the same arch/scope preference as the main
// app. Identifiers that fail to resolve (or have no installer) are
// returned in the unresolved list so the caller can surface them.
func resolveWingetDeps(ctx context.Context, src wingetsrc.Searcher, ids []string, arch, scope string) ([]wingetsrc.DepInstaller, []string) {
	var deps []wingetsrc.DepInstaller
	var unresolved []string
	for _, id := range ids {
		dm, err := src.Manifest(ctx, id, "")
		if err != nil {
			unresolved = append(unresolved, id)
			continue
		}
		di := wingetsrc.PickInstaller(dm.Installers, arch, scope)
		if di == nil || strings.TrimSpace(di.URL) == "" {
			unresolved = append(unresolved, id)
			continue
		}
		deps = append(deps, wingetsrc.DepInstaller{Manifest: dm, Installer: di})
	}
	return deps, unresolved
}

func handleSoftwareImportStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, payload.SoftwareImportStatusFor(id))
	}
}

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/mscatalog"
	"github.com/rusketh/autodeploy/server/internal/payload"
)

// RegisterMSCatalog mounts the operator-driven "import from Microsoft"
// routes: catalog search, import kickoff, and import progress. Everything
// here runs on explicit operator action — nothing downloads automatically.
func RegisterMSCatalog(mux *http.ServeMux, r Repos) {
	if r.MSCatalog == nil || r.Updates == nil || r.Blobs == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/mscatalog/search", requireAuth(r, handleCatalogSearch(r)))
	mux.HandleFunc("POST /api/v1/updates/import", requireAuth(r, handleUpdateImport(r)))
	mux.HandleFunc("GET /api/v1/updates/{id}/import-status", requireAuth(r, handleUpdateImportStatus(r)))
}

func handleCatalogSearch(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		if q == "" || len(q) > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "q query parameter required (max 200 chars)"})
			return
		}
		results, err := r.MSCatalog.Search(req.Context(), q)
		if err != nil {
			// Upstream (Microsoft) failure, not ours.
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		if results == nil {
			results = []mscatalog.Result{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

var reUpdateGUID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// handleUpdateImport creates (or reuses) a windows_update row for a
// catalog search result and starts the background payload download. The
// resulting record is identical to a manually created + uploaded one.
func handleUpdateImport(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			UpdateGUID     string `json:"update_guid"`
			Title          string `json:"title"`
			Products       string `json:"products"`
			Classification string `json:"classification"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		guid := strings.ToLower(strings.TrimSpace(in.UpdateGUID))
		if !reUpdateGUID.MatchString(guid) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "update_guid must be a catalog GUID"})
			return
		}
		title := strings.TrimSpace(in.Title)
		kb := mscatalog.ParseKB(title)
		if kb == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "title carries no KB number; only KB updates can be imported"})
			return
		}

		status := http.StatusCreated
		u, err := r.Updates.Create(req.Context(), model.WindowsUpdate{
			KBNumber:    kb,
			Title:       title,
			Description: strings.TrimSpace(in.Products),
			OSFilter:    mscatalog.GuessOSFilter(in.Products),
			Severity:    mscatalog.GuessSeverity(in.Classification),
		})
		if errors.Is(err, model.ErrConflict) {
			// The KB is already tracked. With a payload that's a real
			// duplicate; without one, treat this import as the retry/upload
			// for the existing row.
			existing, gerr := r.Updates.GetByKB(req.Context(), kb)
			if gerr != nil {
				writeError(w, gerr)
				return
			}
			if existing.StoragePath != "" {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":       kb + " already exists",
					"existing_id": existing.ID,
				})
				return
			}
			u, status = existing, http.StatusOK
		} else if err != nil {
			writeError(w, err)
			return
		}

		payload.StartUpdateImportAsync(r.Blobs, r.Updates, u.ID,
			func(ctx context.Context) ([]string, error) {
				return r.MSCatalog.DownloadLinks(ctx, guid)
			},
			mscatalog.PickFile)
		writeJSON(w, status, map[string]any{"id": u.ID, "kb_number": kb})
	}
}

func handleUpdateImportStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, payload.ImportStatusFor(id))
	}
}

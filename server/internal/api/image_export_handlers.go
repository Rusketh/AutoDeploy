package api

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/rusketh/autodeploy/server/internal/payload"
)

// Image ISO export: author a bootable USB/ISO for an image so operators can
// Rufus it onto a stick and re-image machines whose disk is too small to stage
// the media locally (the normal deploy stages a ~7 GiB media copy onto the
// target's own disk). The build runs in the background; the portal polls
// /export/status and downloads the result from /export/download.

// ExportStartResponse is returned by the start endpoint.
type ExportStartResponse struct {
	Started  bool                 `json:"started"`
	Status   payload.ExportStatus `json:"status"`
	Warnings []string             `json:"warnings,omitempty"`
}

// handleExportImage kicks off a background ISO export for the image.
func handleExportImage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Resolver == nil || r.Blobs == nil {
			http.Error(w, "image export is not available (storage not configured)", http.StatusServiceUnavailable)
			return
		}
		if !payload.ISOBuilderAvailable() {
			http.Error(w, payload.ISOBuilderMissingHint, http.StatusPreconditionFailed)
			return
		}
		outPath, err := payload.ExportOutputPath(r.Blobs, id)
		if err != nil {
			writeError(w, err)
			return
		}

		var warnings []string
		// Bake the current Windows agent so the imaged machine enrols and gets
		// its name reconciled. Without an agent binary in the downloads dir the
		// ISO still images Windows, but the machine stays unmanaged and unnamed.
		agentPath := ""
		if fn, _, _, _ := agentBinaryForArch(r, "windows", "amd64"); fn != "" {
			if dir := downloadsDirFor(r); dir != "" {
				agentPath = filepath.Join(dir, fn)
			}
		}
		if agentPath == "" {
			warnings = append(warnings, "no Windows agent binary found in the downloads directory — the exported ISO will image Windows but the machine will not enrol or be auto-named; add the agent under Downloads and re-export")
		}

		deps := payload.ISOExportDeps{
			Resolver: r.Resolver,
			Blobs:    r.Blobs,
			Drivers:  r.Drivers,
		}
		opts := payload.ISOExportOptions{
			ImageID:         id,
			BaseURL:         baseURLFromRequest(req),
			OutPath:         outPath,
			AgentBinaryPath: agentPath,
		}
		started := payload.StartExportAsync(deps, opts)
		writeJSON(w, http.StatusAccepted, ExportStartResponse{
			Started:  started,
			Status:   payload.ExportStatusFor(id),
			Warnings: warnings,
		})
	}
}

// handleExportStatus reports the background export progress for the image.
func handleExportStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, payload.ExportStatusFor(id))
	}
}

// handleExportDownload streams the finished ISO for the image.
func handleExportDownload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Blobs == nil {
			http.Error(w, "storage not configured", http.StatusServiceUnavailable)
			return
		}
		// Prefer the tracker's recorded path; fall back to the stable location
		// so a download still works after a server restart cleared the tracker.
		path := payload.ExportStatusFor(id).OutputPath
		if path == "" {
			if p, perr := payload.ExportOutputPath(r.Blobs, id); perr == nil {
				path = p
			}
		}
		if path == "" {
			http.Error(w, "no exported ISO for this image — start an export first", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="autodeploy-image-%d.iso"`, int64(id)))
		http.ServeFile(w, req, path)
	}
}

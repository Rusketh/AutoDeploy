package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
)

// Image ISO export: author a bootable USB/ISO for an image so operators can
// Rufus it onto a stick and re-image machines whose disk is too small to stage
// the media locally (the normal deploy stages a ~7 GiB media copy onto the
// target's own disk). The build runs in the background; the portal polls
// /export/status, can stop it with /export/cancel, and downloads the result
// from /export/download.

// ExportStartResponse is returned by the start endpoint.
type ExportStartResponse struct {
	Started  bool                 `json:"started"`
	Status   payload.ExportStatus `json:"status"`
	Warnings []string             `json:"warnings,omitempty"`
}

// Operator-facing download messages (plain text: the portal links straight to
// the download URL, so the browser shows these as-is).
const (
	exportStillBuildingMsg = "This image's ISO export is still building. Wait for the progress bar on the image's Resolved page to reach \"Ready\", then download it."
	exportMissingMsg       = "There is no finished ISO export for this image yet. Open the image's Resolved page and click \"Build ISO\" first."
)

func exportDeps(r Repos) payload.ISOExportDeps {
	return payload.ISOExportDeps{
		Resolver: r.Resolver,
		Blobs:    r.Blobs,
		Drivers:  r.Drivers,
	}
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
		// An export already running is not an error: report its status, as
		// before, without re-validating (the image may be mid-edit).
		if st := payload.ExportStatusFor(id); st.Active() {
			writeJSON(w, http.StatusAccepted, ExportStartResponse{Started: false, Status: st})
			return
		}
		deps := exportDeps(r)
		// Fail fast, with a message the operator can act on, for problems with
		// the image itself — notably an unattend edition the ISO doesn't carry,
		// which would otherwise only surface on the target as Setup's "no
		// images are available".
		if _, err := deps.PreflightExport(req.Context(), id); err != nil {
			var pe *payload.ExportPreflightError
			if errors.As(err, &pe) {
				http.Error(w, pe.Msg, http.StatusUnprocessableEntity)
				return
			}
			writeError(w, err)
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

// exportStatus is the tracker's status, filled in from a finished ISO already
// on disk: when the tracker has nothing (idle: no export since the server
// started) it reports "done", so the portal offers the download after a
// restart and for ISOs exported by a previous server version; after a failed
// or cancelled re-export it keeps the error but carries the previous ISO's
// size/time, which is still downloadable.
func exportStatus(r Repos, id model.ID) payload.ExportStatus {
	st := payload.ExportStatusFor(id)
	if (st.Phase != payload.ExportIdle && st.Phase != payload.ExportError) || r.Blobs == nil {
		return st
	}
	path, err := payload.ExportOutputPath(r.Blobs, id)
	if err != nil {
		return st
	}
	fi, ok := payload.FinishedExport(path)
	if !ok {
		return st
	}
	built := fi.ModTime()
	if st.Phase == payload.ExportError {
		st.SizeBytes, st.BuiltAt = fi.Size(), &built
		return st
	}
	return payload.ExportStatus{
		Phase: payload.ExportDone, Stage: "Done", Percent: 100, Finished: true,
		OutputPath: path, SizeBytes: fi.Size(), BuiltAt: &built,
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
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, exportStatus(r, id))
	}
}

// handleExportCancel stops a running export. Idempotent: cancelling when
// nothing is running reports cancelled=false with the current status.
func handleExportCancel(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		cancelled := payload.CancelExport(id)
		writeJSON(w, http.StatusOK, map[string]any{
			"cancelled": cancelled,
			"status":    exportStatus(r, id),
		})
	}
}

// handleExportDownload streams the finished ISO for the image. It only ever
// serves the final path, which StartExportAsync fills by atomic rename, so a
// build in progress (409) or a failed one (404) can never be downloaded as a
// truncated image.
func handleExportDownload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Blobs == nil {
			http.Error(w, "storage not configured", http.StatusServiceUnavailable)
			return
		}
		if payload.ExportStatusFor(id).Active() {
			http.Error(w, exportStillBuildingMsg, http.StatusConflict)
			return
		}
		// Prefer the tracker's recorded path; fall back to the stable location
		// so a download still works after a server restart cleared the tracker
		// (and for ISOs a previous version wrote there).
		path := payload.ExportStatusFor(id).OutputPath
		if path == "" {
			if p, perr := payload.ExportOutputPath(r.Blobs, id); perr == nil {
				path = p
			}
		}
		fi, ok := payload.FinishedExport(path)
		if path == "" || !ok {
			http.Error(w, exportMissingMsg, http.StatusNotFound)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, exportMissingMsg, http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="autodeploy-image-%d.iso"`, int64(id)))
		// ServeContent handles Range (resumable downloads of a multi-GB file).
		http.ServeContent(w, req, "", fi.ModTime(), f)
	}
}

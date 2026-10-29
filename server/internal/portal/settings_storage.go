package portal

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/runtime"
)

// storageEntry is what the template renders per category: the
// operator-configured override (or empty for default), the resolved
// effective path the BlobStore would route to, the default the
// system would use, and a quick disk-check verdict.
type storageEntry struct {
	Category    string
	Description string
	Override    string
	Effective   string
	Default     string
	State       string // "ok" | "missing" | "unreadable" | "default"
	Detail      string
}

func storageForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "settings_storage.html", "Storage", map[string]any{
			"Entries": collectStorageEntries(r),
			"DataDir": dataDirFor(r),
		})
	}
}

func storageSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		// Check every path before saving any: a relative path is rejected
		// beside its field and nothing changes.
		fe := &FormError{Fields: map[string]string{}}
		for _, c := range runtime.StorageCategories {
			val := strings.TrimSpace(req.FormValue("storage_" + c.Category))
			if val != "" && !filepath.IsAbs(val) {
				fe.Fields["storage_"+c.Category] = "Use a full path starting from the root, e.g. /srv/autodeploy/" + c.Category + "."
			}
		}
		if len(fe.Fields) == 0 {
			for _, c := range runtime.StorageCategories {
				val := strings.TrimSpace(req.FormValue("storage_" + c.Category))
				if err := r.Runtime.SetStoragePath(req.Context(), c.Category, val); err != nil {
					fe.Fields["storage_"+c.Category] = friendlyError("", http.StatusUnprocessableEntity, err)
				}
			}
		}
		if len(fe.Fields) > 0 {
			fe.Message = "Couldn't save — fix the highlighted paths below. Nothing was changed."
			entries := collectStorageEntries(r)
			for i := range entries {
				entries[i].Override = strings.TrimSpace(req.FormValue("storage_" + entries[i].Category))
			}
			renderForm(w, req, r, "settings_storage.html", "Storage", fe, map[string]any{
				"Entries": entries,
				"DataDir": dataDirFor(r),
			})
			return
		}
		// Push the new values into the live BlobStore. Any per-
		// category error here means the path is unwritable (likely
		// missing or wrong-permission); the settings are persisted, so
		// surface the warning and let the operator fix the filesystem.
		var firstErr string
		if r.Blobs != nil {
			for cat, e := range r.Runtime.ApplyStorageOverrides(r.Blobs) {
				firstErr = fmt.Sprintf("Saved, but %s can't be used yet: %v", cat, e)
				break
			}
		}
		if firstErr != "" {
			flash(w, "err", firstErr)
		} else {
			flash(w, "ok", "Storage paths saved.")
		}
		http.Redirect(w, req, "/portal/settings/storage", http.StatusFound)
	}
}

// collectStorageEntries builds the per-category rows the template
// renders. Each row carries the override (or empty), the effective
// path, the default the server would fall back to, and a writability
// check so the operator sees missing / unwritable paths.
func collectStorageEntries(r Repos) []storageEntry {
	out := make([]storageEntry, 0, len(runtime.StorageCategories))
	dataDir := dataDirFor(r)
	for _, c := range runtime.StorageCategories {
		e := storageEntry{
			Category:    c.Category,
			Description: c.Description,
			Default:     filepath.Join(dataDir, c.Category),
		}
		if r.Runtime != nil {
			e.Override = r.Runtime.StoragePath(c.Category)
		}
		if r.Blobs != nil {
			e.Effective = r.Blobs.CategoryRoot(c.Category)
		} else {
			e.Effective = e.Default
		}
		e.State, e.Detail = checkStoragePath(e.Effective)
		if e.Override == "" {
			// Default isn't an "issue"; mark explicitly so the badge
			// can render neutrally.
			if e.State == "ok" {
				e.State = "default"
			}
		}
		out = append(out, e)
	}
	return out
}

func dataDirFor(r Repos) string {
	if r.DataDir != "" {
		return r.DataDir
	}
	if r.Blobs != nil {
		return r.Blobs.Root()
	}
	return ""
}

// checkStoragePath stats the directory and returns a short verdict.
// ok = exists + writable; missing = doesn't exist (the operator can
// create it before flipping the override); unreadable = exists but
// inaccessible.
func checkStoragePath(path string) (string, string) {
	if path == "" {
		return "missing", "no path configured"
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "missing", "directory does not exist yet"
	}
	if err != nil {
		return "unreadable", err.Error()
	}
	if !info.IsDir() {
		return "unreadable", "not a directory"
	}
	// Cheap write probe: try to create+remove a hidden file.
	probe := filepath.Join(path, ".autodeploy-write-probe")
	f, err := os.Create(probe)
	if err != nil {
		return "unreadable", "directory not writable: " + err.Error()
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return "ok", "writable"
}

package portal

import (
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
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		var firstErr string
		for _, c := range runtime.StorageCategories {
			val := strings.TrimSpace(req.FormValue("storage_" + c.Category))
			// Operator confirms a path is absolute before save; reject
			// relative paths early with a clear message rather than
			// have BlobStore.SetCategoryRoot do it silently.
			if val != "" && !filepath.IsAbs(val) {
				if firstErr == "" {
					firstErr = fmt.Sprintf("%s: path must be absolute (got %q)", c.Category, val)
				}
				continue
			}
			if err := r.Runtime.SetStoragePath(req.Context(), c.Category, val); err != nil {
				if firstErr == "" {
					firstErr = fmt.Sprintf("%s: %v", c.Category, err)
				}
				continue
			}
		}
		// Push the new values into the live BlobStore. Any per-
		// category error here means the path is unwritable (likely
		// missing or wrong-permission); we keep going so the
		// settings are persisted, surface the warning, and let the
		// operator fix the filesystem.
		if r.Blobs != nil {
			if errs := r.Runtime.ApplyStorageOverrides(r.Blobs); len(errs) > 0 && firstErr == "" {
				for cat, e := range errs {
					firstErr = fmt.Sprintf("%s: %v", cat, e)
					break
				}
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

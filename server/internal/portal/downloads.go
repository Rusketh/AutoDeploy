package portal

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// downloadKind classifies a file in the downloads directory so the
// page can group/icon them properly. The classification is on
// filename heuristic alone — the operator can drop any artifact and
// it gets the "Other" group if it doesn't match.
type downloadKind int

const (
	dlOther downloadKind = iota
	dlAgent
	dlBoot
	dlServer
	dlInstaller
)

type downloadEntry struct {
	Name        string
	Size        int64
	Modified    time.Time
	Kind        downloadKind
	GroupLabel  string
	GroupIcon   string
	Description string
	Filename    string
}

// downloadGroup is a presentation-layer grouping the template iterates.
type downloadGroup struct {
	Label   string
	Icon    string
	Entries []downloadEntry
}

// downloadsDir is the on-disk root the operator drops distributable
// binaries into. Treated as a flat directory; nested paths are
// served too but the page lists them by filename only. Honours the
// portal-configured storage override for the "downloads" category
// when one is set.
func downloadsDir(r Repos) string {
	if r.Blobs != nil {
		return r.Blobs.CategoryRoot("downloads")
	}
	if r.DataDir == "" {
		return ""
	}
	return filepath.Join(r.DataDir, "downloads")
}

func init() {
	registerDownloadRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/downloads", downloadsList(r))
		get("/portal/downloads/file/{name}", downloadsServe(r))
	}
}

// registerDownloadRoutes is a function pointer wired up at init time
// to the route registration in portal.Register. Matches the pattern
// used by the other registerXRoutes helpers in this package.
var registerDownloadRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {}

func downloadsList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		dir := downloadsDir(r)
		var entries []downloadEntry
		var dirErr string
		if dir == "" {
			dirErr = "data directory not configured"
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			dirErr = err.Error()
		} else {
			items, err := os.ReadDir(dir)
			if err != nil {
				dirErr = err.Error()
			} else {
				for _, it := range items {
					if it.IsDir() {
						continue
					}
					info, err := it.Info()
					if err != nil {
						continue
					}
					entries = append(entries, classifyDownload(it.Name(), info))
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Kind != entries[j].Kind {
				return entries[i].Kind < entries[j].Kind
			}
			return entries[i].Name < entries[j].Name
		})
		// Bucket into groups preserving the per-kind order set above.
		groups := []downloadGroup{}
		for _, e := range entries {
			if len(groups) == 0 || groups[len(groups)-1].Label != e.GroupLabel {
				groups = append(groups, downloadGroup{Label: e.GroupLabel, Icon: e.GroupIcon})
			}
			gi := len(groups) - 1
			groups[gi].Entries = append(groups[gi].Entries, e)
		}
		render(w, req, r, "downloads.html", "Downloads", map[string]any{
			"Groups": groups,
			"Dir":    dir,
			"DirErr": dirErr,
			"Any":    len(entries) > 0,
		})
	}
}

// classifyDownload picks the group + icon + description for a file
// based on its name. Kept narrow on purpose: only the artifacts
// AutoDeploy itself ships get pretty treatment; anything else falls
// through to the generic "Other" group with a download button.
func classifyDownload(name string, info os.FileInfo) downloadEntry {
	low := strings.ToLower(name)
	d := downloadEntry{
		Name:       name,
		Filename:   name,
		Size:       info.Size(),
		Modified:   info.ModTime(),
		Kind:       dlOther,
		GroupLabel: "Other artifacts",
		GroupIcon:  "i-file",
	}
	switch {
	case strings.HasPrefix(low, "autodeploy-agent-"):
		d.Kind = dlAgent
		d.GroupLabel = "Deployment agent (in-OS)"
		d.GroupIcon = "i-box"
		d.Description = describeArchSuffix(low, "autodeploy-agent-")
	case strings.HasPrefix(low, "autodeploy-boot-"):
		d.Kind = dlBoot
		d.GroupLabel = "Boot Client (pre-OS)"
		d.GroupIcon = "i-cpu"
		d.Description = describeArchSuffix(low, "autodeploy-boot-")
	case strings.HasPrefix(low, "autodeploy-server-"):
		d.Kind = dlServer
		d.GroupLabel = "Server binary"
		d.GroupIcon = "i-server"
		d.Description = describeArchSuffix(low, "autodeploy-server-")
	case strings.HasSuffix(low, ".ps1") || strings.HasSuffix(low, ".sh") || strings.HasSuffix(low, ".cmd"):
		d.Kind = dlInstaller
		d.GroupLabel = "Installer scripts"
		d.GroupIcon = "i-gear"
	}
	return d
}

// describeArchSuffix turns "autodeploy-agent-windows-amd64.exe" into
// "windows / amd64" for a friendly subtitle. Strips a trailing .exe.
func describeArchSuffix(name, prefix string) string {
	rest := strings.TrimPrefix(name, prefix)
	rest = strings.TrimSuffix(rest, ".exe")
	rest = strings.TrimSuffix(rest, ".sha256")
	if i := strings.LastIndex(rest, "-"); i > 0 {
		os, arch := rest[:i], rest[i+1:]
		return os + " / " + arch
	}
	return rest
}

func downloadsServe(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		// Sanitise: refuse anything that would resolve outside the
		// downloads directory. The PathValue is already URL-decoded;
		// strip slashes and "..".
		if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			renderPortalError(w, req, r, http.StatusBadRequest,
				"Invalid filename", "Download names must be plain filenames; no paths.", "")
			return
		}
		dir := downloadsDir(r)
		if dir == "" {
			renderPortalError(w, req, r, http.StatusServiceUnavailable,
				"Downloads not configured", "The server has no data directory configured, so downloads can't be served.", "")
			return
		}
		full := filepath.Join(dir, name)
		// Defence-in-depth: ensure the resolved path stays inside dir.
		if !strings.HasPrefix(full, dir+string(os.PathSeparator)) {
			renderPortalError(w, req, r, http.StatusBadRequest,
				"Invalid filename", "Path resolves outside the downloads directory.", "")
			return
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			renderPortalError(w, req, r, http.StatusNotFound,
				"Download not found",
				fmt.Sprintf("There is no file named %q in the downloads directory. Drop one into %s and try again.", name, dir),
				"")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, req, full)
	}
}

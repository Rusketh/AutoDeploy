package portal

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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
	// Unreadable is set when the server process can't open the file
	// (typically a binary copied in by hand as root with 0600 perms). The
	// page flags it instead of offering a download that would fail.
	Unreadable bool
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
					e := classifyDownload(it.Name(), info)
					e.Unreadable = !downloadReadable(filepath.Join(dir, it.Name()))
					entries = append(entries, e)
				}
			}
		}
		var unreadable []string
		for _, e := range entries {
			if e.Unreadable {
				unreadable = append(unreadable, e.Filename)
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
			// Files listed but not readable by the server, plus the
			// account it runs as and a fix command for the banner.
			"Unreadable":    unreadable,
			"ServiceUser":   serviceUser(),
			"PermissionFix": permissionFix(dir, unreadable),
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
		// Open before committing any download headers, so a failure renders
		// an explanatory page rather than a bare status the browser reports
		// only as "Failed - Forbidden".
		f, err := openDownload(full)
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				renderPortalError(w, req, r, http.StatusNotFound,
					"Download not found",
					fmt.Sprintf("There is no file named %q in the downloads directory. Drop one into %s and try again.", name, dir),
					"")
			case errors.Is(err, fs.ErrPermission):
				renderPortalError(w, req, r, http.StatusInternalServerError,
					"The server can't read this file",
					fmt.Sprintf("%s is in the downloads directory, but the AutoDeploy server (running as %s) doesn't have permission to read it. This usually means it was copied in by hand as another user. Fix its ownership on the server, then try again.", name, serviceUser()),
					permissionFix(dir, []string{name}))
			default:
				renderPortalError(w, req, r, http.StatusInternalServerError,
					"Couldn't open download", fmt.Sprintf("Opening %s failed.", name), err.Error())
			}
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			renderPortalError(w, req, r, http.StatusNotFound,
				"Download not found",
				fmt.Sprintf("There is no file named %q in the downloads directory. Drop one into %s and try again.", name, dir),
				"")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, req, name, info.ModTime(), f)
	}
}

// openDownload opens a file for serving. A package var so tests can
// simulate a permission failure (tests usually run as root, which can
// read anything).
var openDownload = func(path string) (*os.File, error) { return os.Open(path) }

// downloadReadable reports whether the server can open path for reading.
func downloadReadable(path string) bool {
	f, err := openDownload(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// serviceUser names the account the server runs as, for error messages.
func serviceUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "the AutoDeploy service account"
}

// permissionFix is the command an operator can run on the server to make
// the named downloads readable again. Empty when nothing needs fixing or
// on Windows, where ownership is fixed through the file's Security tab.
func permissionFix(dir string, names []string) string {
	if len(names) == 0 || runtime.GOOS == "windows" {
		return ""
	}
	u := serviceUser()
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = shellQuote(filepath.Join(dir, n))
	}
	list := strings.Join(paths, " ")
	return fmt.Sprintf("sudo chown %s %s && sudo chmod 0644 %s", shellQuote(u), list, list)
}

// shellQuote single-quotes s for a POSIX shell when it contains anything
// beyond a conservative safe set.
func shellQuote(s string) string {
	safe := true
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-+:@", c)) {
			safe = false
			break
		}
	}
	if safe && s != "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

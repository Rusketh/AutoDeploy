package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// updatesPageData drives the Settings -> Updates page. The page is
// read-only -- it shows what's available, links to the docs for the
// upgrade flow, and lets the operator open the GitHub release in a
// new tab. The actual binary swap is operator-driven via the install
// script.
type updatesPageData struct {
	ServerVersion        string
	LatestServerVersion  string
	LatestServerURL      string
	LatestServerChecked  time.Time
	LatestServerError    string
	AgentAvailable       []agentAvailability
	// UpdaterAvailable is true when the in-place update helper is
	// installed on this server, which gates whether the "Update"
	// button appears. The path itself is shown alongside so the
	// operator can verify exactly what would run.
	UpdaterAvailable bool
	UpdaterPath      string
}

// agentAvailability is one row in the "agents in your downloads
// directory" table. Operators populate the downloads directory; the
// page shows what's there with the embedded version + SHA-256.
type agentAvailability struct {
	Filename string
	Version  string
	SHA256   string
	Size     int64
	Modified time.Time
}

func updatesPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		data := updatesPageData{
			ServerVersion: serverVersionFor(r),
		}
		// Server: probe GitHub Releases for the latest published
		// tag. Best-effort; failures land in LatestServerError and
		// the page still renders with the local version.
		if latest, url, err := fetchLatestServerRelease(req.Context()); err != nil {
			data.LatestServerError = err.Error()
		} else {
			data.LatestServerVersion = latest
			data.LatestServerURL = url
			data.LatestServerChecked = time.Now()
		}
		// Agents: scan the downloads dir for any
		// autodeploy-agent-* binary with a .version sidecar so the
		// page shows what agents the resident check-in loops would
		// be advertised.
		data.AgentAvailable = scanAvailableAgents(r)
		// Whether the in-place updater is installed. The path is
		// platform-fixed and hard-coded in updater_path_*.go so
		// nothing operator-supplied gets executed by the portal.
		data.UpdaterPath = updaterHelperPath()
		if _, err := os.Stat(data.UpdaterPath); err == nil {
			data.UpdaterAvailable = true
		}
		render(w, req, r, "settings_updates.html", "Updates", data)
	}
}

// serverVersionFor pulls the version from the api package. The api
// package is the owner of the build-time Version constant; we read
// it via the same SetServerVersion ServerVersion the version endpoint
// returns so the page stays consistent with /api/v1/version.
func serverVersionFor(r Repos) string {
	if r.ServerVersion != "" {
		return r.ServerVersion
	}
	return "dev"
}

// fetchLatestServerRelease asks GitHub for the latest release of the
// autodeploy repository. Returns "" if no release exists yet (e.g.
// fresh project, or network unreachable). The repo is hard-coded
// against Rusketh/AutoDeploy since the brand is global; multi-fork
// installs can ignore this surface.
func fetchLatestServerRelease(ctx context.Context) (string, string, error) {
	const url = "https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", err
	}
	// Identify ourselves so GitHub's rate limiting doesn't catch
	// an empty UA.
	req.Header.Set("User-Agent", "autodeploy-server")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// No release yet -- normal for a fresh repo.
		return "", "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", &httpErr{resp.StatusCode, resp.Status}
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	return body.TagName, body.HTMLURL, nil
}

type httpErr struct {
	code int
	msg  string
}

func (e *httpErr) Error() string { return e.msg }

// scanAvailableAgents enumerates the downloads directory for
// agent binaries plus their version sidecars and SHA-256 sidecars.
// The page renders one row per (binary, version) pair so the
// operator can see what's queued up.
func scanAvailableAgents(r Repos) []agentAvailability {
	dir := downloadsDir(r)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []agentAvailability{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "autodeploy-agent-") {
			continue
		}
		// Skip the sidecar files themselves.
		if strings.HasSuffix(name, ".version") || strings.HasSuffix(name, ".sha256") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		a := agentAvailability{
			Filename: name,
			Size:     info.Size(),
			Modified: info.ModTime(),
		}
		if vbytes, verr := os.ReadFile(filepath.Join(dir, name+".version")); verr == nil {
			a.Version = strings.TrimSpace(string(vbytes))
		}
		if sbytes, serr := os.ReadFile(filepath.Join(dir, name+".sha256")); serr == nil {
			for _, line := range strings.Split(string(sbytes), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if i := strings.Index(line, " "); i > 0 {
					a.SHA256 = line[:i]
				} else {
					a.SHA256 = line
				}
				break
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		// Sort by version descending; entries without a version
		// (operator-dropped binaries) drop to the bottom.
		if out[i].Version != out[j].Version {
			if out[i].Version == "" {
				return false
			}
			if out[j].Version == "" {
				return true
			}
			return out[i].Version > out[j].Version
		}
		return out[i].Filename < out[j].Filename
	})
	return out
}

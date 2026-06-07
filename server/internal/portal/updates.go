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
	ServerVersion       string
	LatestServerVersion string
	LatestServerURL     string
	LatestServerChecked time.Time
	LatestServerError   string
	AgentAvailable      []agentAvailability
	// UpdateState is one of "up_to_date", "older", "ahead",
	// "pre_release" or "unknown". The template branches on it
	// instead of comparing strings, so dev/PR builds (which are
	// never equal to any tag) don't get presented as if they're
	// behind by one patch.
	UpdateState string
	// UpdaterAvailable is true when the in-place update helper is
	// installed on this server, which gates whether the "Update"
	// button appears. The path itself is shown alongside so the
	// operator can verify exactly what would run.
	UpdaterAvailable bool
	UpdaterPath      string
	// AgentForOS / AgentForArch is what the "Install agent" button
	// will fetch. Defaults to the operator's GOOS/GOARCH (since the
	// resident agent is the same arch as the running OS being
	// deployed -- nearly always windows/amd64 in practice). The
	// page lets the operator pick from a dropdown so a mixed fleet
	// can pull arm64 too.
	AgentForOS   string
	AgentForArch string
	// AgentMissing is true when no agent binary for AgentForOS/Arch
	// is currently present in the downloads directory.
	AgentMissing bool
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
		// Compare running vs latest using a semver-aware comparator
		// instead of plain string equality so dev/PR builds (whose
		// running version doesn't parse) get a clearly-labelled
		// "pre_release" state instead of being framed as if they're
		// behind by one patch.
		data.UpdateState = computeUpdateState(data.ServerVersion, data.LatestServerVersion)
		// Whether the in-place updater is installed. The path is
		// platform-fixed and hard-coded in updater_path_*.go so
		// nothing operator-supplied gets executed by the portal.
		data.UpdaterPath = updaterHelperPath()
		if _, err := os.Stat(data.UpdaterPath); err == nil {
			data.UpdaterAvailable = true
		}
		// Default agent target: windows/amd64 (the common case the
		// agent ships for). Operator can override via query string
		// when picking a different arch from the dropdown so the
		// "Install agent" button POSTs against the same selection.
		data.AgentForOS = strings.TrimSpace(req.URL.Query().Get("agent_os"))
		data.AgentForArch = strings.TrimSpace(req.URL.Query().Get("agent_arch"))
		if data.AgentForOS == "" {
			data.AgentForOS = "windows"
		}
		if data.AgentForArch == "" {
			data.AgentForArch = "amd64"
		}
		data.AgentMissing = !agentPresent(r, data.AgentForOS, data.AgentForArch)
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

// computeUpdateState decides which of the three terminal states the
// Settings -> Updates page should render. The states are:
//
//   - "up_to_date": running == latest (semver-aware, so v1.0 == v1.0.0
//     and pre-release tags don't trick the comparison).
//   - "older":      running parses as semver and is strictly behind.
//   - "ahead":      running parses as semver and is strictly ahead.
//   - "pre_release": running doesn't parse as semver -- a dev build,
//     a build-from-source on a feature branch, or a non-tagged
//     commit. The page renders an explicit "Pre-release build" badge
//     and offers an "Install latest release vX.Y.Z" button (which
//     would replace the dev binary with the latest tagged build --
//     downgrade-aware messaging is on the operator).
//   - "unknown":    no latest version available (offline portal,
//     GitHub API unreachable, no releases yet, etc).
func computeUpdateState(running, latest string) string {
	if latest == "" {
		return "unknown"
	}
	runningOK := isSemverString(running)
	if !runningOK {
		return "pre_release"
	}
	if normalizeSemver(running) == normalizeSemver(latest) {
		return "up_to_date"
	}
	if semverStrLess(running, latest) {
		return "older"
	}
	return "ahead"
}

// isSemverString returns true when s parses as vMAJOR.MINOR.PATCH
// (plus an optional pre-release suffix). Mirrors the api package's
// looksLikeSemver but local to portal so the two packages stay
// independent.
func isSemverString(s string) bool {
	if !strings.HasPrefix(s, "v") {
		return false
	}
	core := s[1:]
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// normalizeSemver strips a leading v and any pre-release/build
// metadata so "v1.0.0", "1.0.0" and "v1.0.0+xyz" compare equal.
func normalizeSemver(s string) string {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	return s
}

// semverStrLess wraps the api package's semantic compare. Lives
// here so portal doesn't depend on api directly.
func semverStrLess(a, b string) bool {
	maA, miA, paA, _, okA := parseSemverParts(a)
	maB, miB, paB, _, okB := parseSemverParts(b)
	if !okA || !okB {
		return false
	}
	if maA != maB {
		return maA < maB
	}
	if miA != miB {
		return miA < miB
	}
	return paA < paB
}

func parseSemverParts(v string) (major, minor, patch int, pre string, ok bool) {
	v = strings.TrimPrefix(v, "v")
	core := v
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		core = v[:i]
		pre = v[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) < 3 {
		return 0, 0, 0, "", false
	}
	parsePart := func(s string) (int, bool) {
		if s == "" {
			return 0, false
		}
		n := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + int(c-'0')
		}
		return n, true
	}
	var ok2 bool
	if major, ok2 = parsePart(parts[0]); !ok2 {
		return 0, 0, 0, "", false
	}
	if minor, ok2 = parsePart(parts[1]); !ok2 {
		return 0, 0, 0, "", false
	}
	if patch, ok2 = parsePart(parts[2]); !ok2 {
		return 0, 0, 0, "", false
	}
	return major, minor, patch, pre, true
}

// agentPresent reports whether the downloads directory already has
// an autodeploy-agent-<os>-<arch> binary. Used to decide whether the
// "Install agent" button is offered as a fresh install or as a
// "refresh from latest release" action.
func agentPresent(r Repos, goos, goarch string) bool {
	dir := downloadsDir(r)
	if dir == "" {
		return false
	}
	name := agentFilename(goos, goarch)
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		return true
	}
	return false
}

// agentFilename matches the release artifact naming convention.
// Centralised here so the install-agent handler, the agent
// availability table, and the api/agent/update-info endpoint can't
// drift apart.
func agentFilename(goos, goarch string) string {
	if goos == "windows" {
		return "autodeploy-agent-" + goos + "-" + goarch + ".exe"
	}
	return "autodeploy-agent-" + goos + "-" + goarch
}

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

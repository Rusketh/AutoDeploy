package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VersionInfo is the payload returned by GET /api/v1/version. The
// portal Settings -> Updates page renders it; CI tooling can poll it
// to compare against a desired version.
type VersionInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// ServerVersion is set by main.go on startup so the api package can
// surface it without circular imports.
var ServerVersion = "dev"

// SetServerVersion is called from main.go after build-time version
// resolution. Calling once at startup is sufficient; the value is
// read concurrently from request handlers.
func SetServerVersion(v string) {
	if v == "" {
		v = "dev"
	}
	versionMu.Lock()
	ServerVersion = v
	versionMu.Unlock()
}

var versionMu sync.RWMutex

func currentServerVersion() string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return ServerVersion
}

// RegisterVersion mounts the version and agent-update endpoints. The
// version endpoint is open (clients use it to confirm the URL is the
// server they think it is); the agent update endpoint accepts the
// agent's identity + current version and returns whatever the
// downloads directory currently advertises.
func RegisterVersion(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/version", handleVersion())
	mux.HandleFunc("POST /api/v1/agent/update-info", handleAgentUpdateInfo(r))
	mux.HandleFunc("GET /api/v1/agent/update-info", handleAgentUpdateInfo(r))
}

func handleVersion() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, VersionInfo{
			Version:   currentServerVersion(),
			GoVersion: runtime.Version(),
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
		})
	}
}

// AgentUpdateInfo is the response shape the agent uses to decide
// whether to self-update.
type AgentUpdateInfo struct {
	// Current is the version the server thinks the agent ought to be
	// running, derived from the downloads directory. Empty when no
	// agent binary is available for the requesting platform.
	Current string `json:"current_version"`
	// UpdateAvailable is true when the agent's reported running
	// version is strictly older than Current. Compared with a
	// minimal semver-aware comparator; non-semver versions ("dev")
	// always trigger an update.
	UpdateAvailable bool `json:"update_available"`
	// URL is the absolute download URL for the new binary. Empty
	// when no update is available.
	URL string `json:"url,omitempty"`
	// SHA256 is the expected hex-encoded SHA-256 of the new binary.
	// The agent MUST verify this before swapping the running
	// executable -- this is the only authenticator the server has
	// against a tampered binary.
	SHA256 string `json:"sha256,omitempty"`
	// Size is the expected byte count of the new binary; lets the
	// agent log progress and detect a truncated download before
	// computing the hash.
	Size int64 `json:"size_bytes,omitempty"`
}

type agentUpdateRequest struct {
	// OS / Arch let one server serve agents for multiple platforms.
	// Empty defaults to "windows" / "amd64" since that's the
	// supported deployment target.
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	CurrentVersion  string `json:"current_version"`
}

func handleAgentUpdateInfo(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in agentUpdateRequest
		if req.Method == http.MethodPost && req.ContentLength > 0 {
			_ = decodeJSON(req, &in)
		}
		if in.OS == "" {
			in.OS = "windows"
		}
		if in.Arch == "" {
			in.Arch = "amd64"
		}
		filename, version, sha, size := agentBinaryForArch(r, in.OS, in.Arch)
		info := AgentUpdateInfo{Current: version}
		if filename != "" && version != "" {
			info.URL = baseURLFromRequest(req) + "/portal/downloads/file/" + filename
			info.SHA256 = sha
			info.Size = size
			info.UpdateAvailable = isOlderVersion(in.CurrentVersion, version)
		}
		writeJSON(w, http.StatusOK, info)
	}
}

// agentBinaryForArch picks the newest agent binary in the downloads
// directory for the given (os, arch). The "newest" is whichever
// .version sidecar parses to the highest semver. Returns filename,
// version, sha256 (hex), size, or empty strings if nothing matches.
func agentBinaryForArch(r Repos, goos, goarch string) (filename, version, sha string, size int64) {
	dir := downloadsDirFor(r)
	if dir == "" {
		return "", "", "", 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", "", 0
	}
	// Filename pattern: autodeploy-agent-<os>-<arch>(.exe)?
	suffix := goos + "-" + goarch
	if goos == "windows" {
		suffix += ".exe"
	}
	wantPrefix := "autodeploy-agent-"
	type candidate struct {
		name, version, sha string
		size               int64
	}
	var pool []candidate
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, wantPrefix) {
			continue
		}
		// Strict-match the exact platform suffix so windows-amd64
		// doesn't accidentally match windows-arm64.
		if !strings.HasSuffix(n, suffix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		// Read .version sidecar.
		vbytes, verr := os.ReadFile(filepath.Join(dir, n+".version"))
		if verr != nil {
			continue
		}
		v := strings.TrimSpace(string(vbytes))
		if v == "" || v == "dev" {
			continue
		}
		// Read or compute sha256.
		hash := readSHA256Sidecar(filepath.Join(dir, n+".sha256"), n)
		if hash == "" {
			hash = computeSHA256(filepath.Join(dir, n))
		}
		pool = append(pool, candidate{n, v, hash, info.Size()})
	}
	if len(pool) == 0 {
		return "", "", "", 0
	}
	sort.Slice(pool, func(i, j int) bool {
		return semverLess(pool[j].version, pool[i].version)
	})
	c := pool[0]
	return c.name, c.version, c.sha, c.size
}

// downloadsDirFor returns the operator-configured downloads
// directory, falling back to <data dir>/downloads. The api package
// keeps a private copy of this resolution rather than importing
// portal to avoid a dependency cycle.
func downloadsDirFor(r Repos) string {
	if r.Blobs != nil {
		return r.Blobs.CategoryRoot("downloads")
	}
	return ""
}

// readSHA256Sidecar reads a "<hex>  <filename>" line from path. The
// release workflow writes one per binary; if missing we recompute.
func readSHA256Sidecar(path, expectedFilename string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 1 {
			continue
		}
		if len(parts) >= 2 {
			// "<hex>  <filename>" -- accept either filename or
			// basename match.
			if filepath.Base(parts[1]) != expectedFilename && parts[1] != expectedFilename {
				continue
			}
		}
		return parts[0]
	}
	return ""
}

// computeSHA256 hashes a file on demand. Used when the release's
// .sha256 sidecar is missing -- which happens if the operator dropped
// a binary into the downloads directory by hand.
func computeSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// semverLess returns true when a < b under a minimal semver
// comparator. Handles the "vMAJOR.MINOR.PATCH" form plus an optional
// pre-release suffix; pre-release tags compare as less than the same
// MAJOR.MINOR.PATCH without one. Non-conforming strings (e.g. "dev")
// compare as -infinity so any tagged version wins.
func semverLess(a, b string) bool {
	majA, minA, patA, preA, okA := parseSemver(a)
	majB, minB, patB, preB, okB := parseSemver(b)
	if !okA && !okB {
		return a < b
	}
	if !okA {
		return true
	}
	if !okB {
		return false
	}
	if majA != majB {
		return majA < majB
	}
	if minA != minB {
		return minA < minB
	}
	if patA != patB {
		return patA < patB
	}
	// Equal major.minor.patch -- compare pre-release. No pre-release
	// > any pre-release per semver.
	if preA == "" && preB != "" {
		return false
	}
	if preA != "" && preB == "" {
		return true
	}
	return preA < preB
}

func parseSemver(v string) (major, minor, patch int, pre string, ok bool) {
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
	var err error
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, "", false
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, "", false
	}
	if patch, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, "", false
	}
	return major, minor, patch, pre, true
}

// isOlderVersion returns true when `running` is strictly older than
// `available`. Used by the update-info handler to set
// UpdateAvailable. An empty or "dev" running version is always
// considered older so a freshly installed agent gets the latest on
// its very next check-in.
func isOlderVersion(running, available string) bool {
	if running == "" || running == "dev" {
		return true
	}
	return semverLess(running, available)
}

func baseURLFromRequest(req *http.Request) string {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + req.Host
}

// unused suppressor so refactors don't strand time imports.
var _ = time.Now

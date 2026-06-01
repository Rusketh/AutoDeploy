package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	mux.HandleFunc("GET /api/v1/agent/download/{name}", handleAgentDownload(r))
	mux.HandleFunc("POST /api/v1/server/update", requireAuth(r, handleServerUpdate(r)))
	mux.HandleFunc("GET /api/v1/server/update-log", requireAuth(r, handleUpdateLog(r)))
	mux.HandleFunc("POST /api/v1/server/install-agent", requireAuth(r, handleInstallAgent(r)))
}

// handleAgentDownload serves an agent binary (or its .sha256/.version
// sidecar) from the downloads directory over a PUBLIC route. The Boot
// Client and the agent's self-update fetch it UNAUTHENTICATED -- the
// portal's /portal/downloads/file path is session-gated, so pointing
// clients there returned the login-page HTML (a ~10 KB "corrupt" .exe).
// Only clean basenames with the agent prefix are served, so this can't be
// used to read arbitrary files.
func handleAgentDownload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if name == "" || name != filepath.Base(name) ||
			strings.Contains(name, "..") || !strings.HasPrefix(name, "autodeploy-agent-") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		dir := downloadsDirFor(r)
		if dir == "" {
			http.Error(w, "downloads not configured", http.StatusNotFound)
			return
		}
		http.ServeFile(w, req, filepath.Join(dir, name))
	}
}

// handleInstallAgent downloads the requested agent binary + sidecars
// from a GitHub release into the operator-configured downloads dir.
// Used by the Settings -> Updates page's "Install agent" button when
// the operator built the server from source and never ran
// install-linux.sh's auto-fetch.
//
// Request body: {"os":"windows","arch":"amd64","tag":"v0.1.9"}.
// All three are optional:
//   - tag: defaults to "latest" via the GitHub releases API.
//   - os/arch: default to windows/amd64 (the agent's primary target).
//
// On success returns the resolved tag, filename, and the SHA-256
// the agent will be verified against, so the page can render an
// "Installed v0.1.9, sha256 abc1234..." confirmation.
func handleInstallAgent(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
			Tag  string `json:"tag"`
		}
		_ = decodeJSON(req, &body)
		if body.OS == "" {
			body.OS = "windows"
		}
		if body.Arch == "" {
			body.Arch = "amd64"
		}
		// Same vMAJOR.MINOR.PATCH gate the in-place update path uses
		// so an attacker can't smuggle a path-traversal-y "tag" into
		// the downloaded URL.
		if body.Tag != "" && !looksLikeSemver(body.Tag) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "tag must look like vMAJOR.MINOR.PATCH",
			})
			return
		}
		// Same OS/Arch whitelist so the page can't ask the server
		// to download a "../../etc/passwd" archive.
		if !isPlainIdent(body.OS) || !isPlainIdent(body.Arch) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "os and arch must be ASCII identifiers (a-z, 0-9, dash)",
			})
			return
		}
		dl := downloadsDirFor(r)
		if dl == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "downloads directory is not configured",
			})
			return
		}
		// Resolve "latest" if no explicit tag came in.
		tag := body.Tag
		if tag == "" {
			latest, err := resolveLatestReleaseTag(req.Context())
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{
					"error": "failed to resolve latest release: " + err.Error(),
				})
				return
			}
			tag = latest
		}
		name := agentReleaseFilename(body.OS, body.Arch)
		base := "https://github.com/Rusketh/AutoDeploy/releases/download/" + tag + "/"
		hash, err := fetchAndVerifyRelease(req.Context(), base, name, dl)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "fetch failed: " + err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "installed",
			"tag":      tag,
			"filename": name,
			"sha256":   hash,
			"path":     filepath.Join(dl, name),
		})
	}
}

// isPlainIdent returns true when s is a non-empty string of
// lower-case ASCII letters, digits and dashes. Restricts what we'll
// concatenate into a URL or filesystem path.
func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// agentReleaseFilename matches the release-asset naming convention.
// Centralised so handleInstallAgent and the agent-update-info
// scanner can't drift apart on the trailing ".exe" detail.
func agentReleaseFilename(goos, goarch string) string {
	if goos == "windows" {
		return "autodeploy-agent-" + goos + "-" + goarch + ".exe"
	}
	return "autodeploy-agent-" + goos + "-" + goarch
}

// resolveLatestReleaseTag returns the tag_name of the most recent
// release. Uses the unauthenticated public API path so air-gapped
// installs fail predictably.
func resolveLatestReleaseTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "autodeploy-server")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("releases/latest returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if !looksLikeSemver(body.TagName) {
		return "", fmt.Errorf("unexpected tag_name %q", body.TagName)
	}
	return body.TagName, nil
}

// fetchAndVerifyRelease downloads the binary + its .sha256 sidecar
// (and the optional .version sidecar) into dlDir, verifies the
// binary's SHA-256 against the sidecar, and returns the verified
// hex hash. On any verification failure the binary is removed so a
// retry has a clean slate.
func fetchAndVerifyRelease(ctx context.Context, urlBase, filename, dlDir string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}

	binPath := filepath.Join(dlDir, filename)
	if err := fetchFile(ctx, client, urlBase+filename, binPath); err != nil {
		return "", err
	}
	shaPath := binPath + ".sha256"
	if err := fetchFile(ctx, client, urlBase+filename+".sha256", shaPath); err != nil {
		_ = os.Remove(binPath)
		return "", fmt.Errorf("fetch .sha256: %w", err)
	}
	// .version sidecar is optional pre-v0.1.3, treat its absence as
	// harmless. Don't fail the whole install if it's missing.
	verPath := binPath + ".version"
	if err := fetchFile(ctx, client, urlBase+filename+".version", verPath); err != nil {
		_ = os.Remove(verPath)
	}

	expected := readSHA256Sidecar(shaPath, filename)
	if expected == "" {
		_ = os.Remove(binPath)
		_ = os.Remove(shaPath)
		return "", errors.New("could not parse expected SHA-256 from sidecar")
	}
	got := computeSHA256(binPath)
	if got != expected {
		_ = os.Remove(binPath)
		_ = os.Remove(shaPath)
		_ = os.Remove(verPath)
		return "", fmt.Errorf("SHA-256 mismatch (got %s, want %s)", got, expected)
	}
	return got, nil
}

func fetchFile(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "autodeploy-server")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// handleServerUpdate triggers the in-place upgrade helper that the
// install script dropped at /usr/local/sbin/autodeploy-update. The
// helper is granted passwordless sudo via /etc/sudoers.d/, so the
// service user (which can't elevate by default) is allowed to launch
// it but nothing else.
//
// The handler returns 202 Accepted immediately and spawns the helper
// detached -- the script's `systemctl stop autodeploy.service` will
// take this process down moments later, so we never get to write a
// "done" response. The portal's button shows a "restarting" indicator
// and polls /api/v1/version after a few seconds to confirm the new
// version is live.
//
// Optional body: {"tag": "v0.1.5"}. Empty body means "latest".
func handleServerUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Same admin-or-better authorization the rest of /api/v1
		// requires; the api package's middleware applies before we
		// land here.
		var body struct {
			Tag string `json:"tag"`
		}
		_ = decodeJSON(req, &body)
		// Tag whitelist: only vMAJOR.MINOR.PATCH with an optional
		// pre-release suffix. Anything else is a non-starter --
		// shells out via the helper which uses --tag.
		if body.Tag != "" && !looksLikeSemver(body.Tag) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "tag must look like vMAJOR.MINOR.PATCH",
			})
			return
		}
		helper := serverUpdateHelperPath()
		if _, err := os.Stat(helper); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "server update helper is not installed at " + helper +
					"; the install script must have been run before this button works",
			})
			return
		}
		// Verify the sudoers entry exists. Without it, sudo -n will
		// fail immediately because there's no tty to prompt for a
		// password. Catch this early with a clear message.
		const sudoersPath = "/etc/sudoers.d/autodeploy-update"
		if _, err := os.Stat(sudoersPath); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "sudoers entry for autodeploy-update is not installed; " +
					"re-run the installer or manually add the entry at " + sudoersPath,
			})
			return
		}

		// Resolve the data directory so the update script uses the
		// same location the server was configured with.
		dataDir := updateDataDir(r)

		// Log file for the spawned process -- lets operators (and
		// the /api/v1/server/update-log endpoint) inspect failures
		// even though the process is detached.
		logPath := filepath.Join(dataDir, "update.log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "failed to open update log at " + logPath + ": " + err.Error(),
			})
			return
		}

		// Spawn detached. The helper's first action is
		// `systemctl stop autodeploy.service`, which will kill us
		// 50ms from now -- so we set Setsid so the child outlives
		// our exit, and we don't wait for it.
		args := []string{"-n", helper, "--data", dataDir}
		if body.Tag != "" {
			args = append(args, "--tag", body.Tag)
		}
		cmd := execCommand("sudo", args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		// Detach: new session so the child isn't in our process
		// group and won't take a SIGTERM with us.
		cmd.SysProcAttr = newDetachedSysProcAttr()
		if err := cmd.Start(); err != nil {
			logFile.Close()
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "failed to start update helper: " + err.Error(),
			})
			return
		}

		pid := cmd.Process.Pid
		// Release the child so the OS reaps it independently.
		_ = cmd.Process.Release()
		logFile.Close()

		// Brief delay to catch immediate failures (e.g. sudo
		// rejecting the command, script syntax error). If the
		// process already exited, read the log for a useful error.
		time.Sleep(100 * time.Millisecond)
		if !processIsRunning(pid) {
			snippet, _ := os.ReadFile(logPath)
			msg := "update helper exited immediately"
			if len(snippet) > 0 {
				// Cap to a reasonable size for the JSON response.
				s := string(snippet)
				if len(s) > 512 {
					s = s[:512] + "…"
				}
				msg += ": " + s
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": msg,
			})
			return
		}

		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "update started; the server will restart in a few seconds",
			"tag":    body.Tag,
		})
	}
}

// updateDataDir returns the data directory the server is using. It
// prefers the BlobStore root (which is the --data flag's value), and
// falls back to the well-known default.
func updateDataDir(r Repos) string {
	if r.Blobs != nil {
		return r.Blobs.Root()
	}
	return "/var/lib/autodeploy"
}

// updateLogPath returns the path to the update log file.
func updateLogPath(r Repos) string {
	return filepath.Join(updateDataDir(r), "update.log")
}

// handleUpdateLog serves the last portion of the update log file so
// operators can diagnose update failures from the dashboard. The log
// is written by the detached update helper process.
func handleUpdateLog(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		logPath := updateLogPath(r)
		data, err := os.ReadFile(logPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"error": "no update log found; no update has been run yet",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "failed to read update log: " + err.Error(),
			})
			return
		}

		// Return the last 64 KiB at most to avoid huge responses.
		const maxBytes = 64 * 1024
		content := string(data)
		truncated := false
		if len(content) > maxBytes {
			content = content[len(content)-maxBytes:]
			truncated = true
		}

		// Parse optional ?lines=N query parameter to limit output
		// to the last N lines.
		if linesStr := req.URL.Query().Get("lines"); linesStr != "" {
			if n, err := strconv.Atoi(linesStr); err == nil && n > 0 {
				lines := strings.Split(content, "\n")
				if len(lines) > n {
					lines = lines[len(lines)-n:]
					truncated = true
				}
				content = strings.Join(lines, "\n")
			}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"log":       content,
			"truncated": truncated,
		})
	}
}

// looksLikeSemver is the same gate the update helper applies on its
// --tag argument, mirrored server-side so we don't shell out with a
// bogus value in the first place.
func looksLikeSemver(s string) bool {
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
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CurrentVersion string `json:"current_version"`
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
			// Public, unauthenticated download route -- NOT the session-gated
			// /portal/downloads/file path (which would hand clients a login
			// page instead of the binary).
			info.URL = baseURLFromRequest(req) + "/api/v1/agent/download/" + filename
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
	// Never advertise an update unless the AVAILABLE build carries a real,
	// parseable semver. A non-semver available ("dev", or a binary with no
	// .version sidecar) cannot be proven newer, and treating it as newer
	// caused a self-update loop: a "dev" agent was told to update to an
	// equally-"dev" binary on every poll, exiting the resident loop each time
	// (so check-ins stopped and queued jobs like re-image were never
	// claimed). Likewise, an identical version is never "older".
	if _, _, _, _, ok := parseSemver(available); !ok {
		return false
	}
	if running == available {
		return false
	}
	// A dev/empty running build is older than any real release -- this
	// updates once (dev -> released) and then settles, because afterwards
	// running == available.
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

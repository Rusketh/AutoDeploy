package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The setup-lock credential provider DLL is distributed exactly like the
// agent binary: built and attached to the GitHub release, fetched into the
// server's downloads directory (alongside the agent, by handleInstallAgent
// and the installer's auto-fetch), served over the public download route,
// and advertised here so the boot client can fetch + inject it at imaging
// time when an image opts into the lock. The operator never builds or places
// the DLL by hand.

// credProviderReleaseFilename matches the release-asset naming convention for
// the setup-lock credential provider. Centralised so handleInstallAgent and
// the credprovider-info scanner can't drift apart. The provider is a Windows
// COM DLL, so the artifact is always a .dll.
func credProviderReleaseFilename(goos, goarch string) string {
	return "autodeploy-credprovider-" + goos + "-" + goarch + ".dll"
}

// CredProviderInfo tells the boot client where to fetch the setup-lock
// credential provider DLL and how to verify it. Mirrors AgentUpdateInfo, minus
// the running-version comparison: the boot client always wants the newest DLL
// when an image opts into the lock (it injects a fresh copy each deploy).
type CredProviderInfo struct {
	// URL is the absolute, public download URL. Empty when no DLL is
	// available (the boot client then deploys without the lock).
	URL string `json:"url,omitempty"`
	// SHA256 is the expected hex-encoded SHA-256; the boot client MUST verify
	// it before injecting the DLL, exactly as it does for the agent binary.
	SHA256 string `json:"sha256,omitempty"`
	// Size is the expected byte count.
	Size int64 `json:"size_bytes,omitempty"`
	// Version is the release version the DLL came from (for logging).
	Version string `json:"version,omitempty"`
}

// handleCredProviderInfo advertises the newest credential provider DLL in the
// downloads directory for the requested platform (defaults windows/amd64).
func handleCredProviderInfo(r Repos) http.HandlerFunc {
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
		filename, version, sha, size := credProviderBinaryForArch(r, in.OS, in.Arch)
		info := CredProviderInfo{}
		if filename != "" {
			// Public, unauthenticated download route -- the same one the agent
			// binary uses (handleAgentDownload also allows the credprovider
			// prefix). NOT the session-gated portal path.
			info.URL = baseURLFromRequest(req) + "/api/v1/agent/download/" + filename
			info.SHA256 = sha
			info.Size = size
			info.Version = version
		}
		writeJSON(w, http.StatusOK, info)
	}
}

// credProviderBinaryForArch picks the newest credential provider DLL in the
// downloads directory for the given (os, arch). Mirrors agentBinaryForArch:
// "newest" is whichever .version sidecar parses to the highest semver.
func credProviderBinaryForArch(r Repos, goos, goarch string) (filename, version, sha string, size int64) {
	dir := downloadsDirFor(r)
	if dir == "" {
		return "", "", "", 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", "", 0
	}
	suffix := goos + "-" + goarch + ".dll"
	const wantPrefix = "autodeploy-credprovider-"
	type candidate struct {
		name, version, sha string
		size               int64
	}
	var pool []candidate
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, wantPrefix) || !strings.HasSuffix(n, suffix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		vbytes, verr := os.ReadFile(filepath.Join(dir, n+".version"))
		if verr != nil {
			continue
		}
		v := strings.TrimSpace(string(vbytes))
		if v == "" || v == "dev" {
			continue
		}
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

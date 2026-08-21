// Package wingetsrc resolves an application from winget's community
// catalog so operators can import it — installer payload plus a ready-made
// detection/install spec — and keep it as a standard, editable
// SoftwarePackage.
//
// This exists because many sites disable the Microsoft Store / winget
// source by policy, which stops the agent's live "winget install" step
// from ever reaching a source. Importing here downloads the real vendor
// installer to the server up front and builds a package that installs
// with plain msi/exe/appx steps — no Store or winget source required on
// the target at install time.
//
// It uses winget's *real* data sources, not a REST API (there is no public
// REST endpoint for the community repo):
//
//   - Search + version discovery come from winget's pre-indexed source: a
//     SQLite index shipped inside an MSIX at cdn.winget.microsoft.com. This
//     is exactly what the winget client itself consumes; see index.go.
//   - Installer details come from the per-version YAML manifests in the
//     microsoft/winget-pkgs GitHub repo, fetched over raw.githubusercontent
//     at a path derived deterministically from the package identifier and
//     version; see manifest.go.
//
// Scope is deliberately the winget *community* source only: true
// Store-exclusive (msstore) apps are DRM/licence-locked and cannot be
// repackaged offline. Everything here is operator-driven; nothing searches
// or downloads on a schedule.
package wingetsrc

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultIndexBaseURL is winget's pre-indexed community source (the
	// "winget" source's Arg). It serves the source index MSIX.
	DefaultIndexBaseURL = "https://cdn.winget.microsoft.com/cache"
	// DefaultRawBaseURL is the raw content root of the winget-pkgs repo,
	// under which every manifest lives at manifests/<c>/<id parts>/<ver>/.
	DefaultRawBaseURL = "https://raw.githubusercontent.com/microsoft/winget-pkgs/master"
	// DefaultIndexTTL is how long a downloaded index is reused before a
	// refresh. The catalog changes slowly relative to an operator's import
	// cadence, and a stale index only means a just-published version might
	// not appear yet.
	DefaultIndexTTL = 24 * time.Hour
)

// Result is one search match, trimmed to what the portal shows and what an
// import needs to resolve the full manifest.
type Result struct {
	PackageIdentifier string `json:"package_identifier"`
	Name              string `json:"name"`
	Publisher         string `json:"publisher"`
	LatestVersion     string `json:"latest_version,omitempty"`
}

// Installer is one installer variant of a package version.
type Installer struct {
	Architecture  string `json:"architecture"`
	InstallerType string `json:"installer_type"`
	Scope         string `json:"scope,omitempty"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256,omitempty"`
	ProductCode   string `json:"product_code,omitempty"`
	// Silent switches, straight from the manifest's InstallerSwitches.
	Silent             string `json:"silent,omitempty"`
	SilentWithProgress string `json:"silent_with_progress,omitempty"`
	Custom             string `json:"custom,omitempty"`
	// AppsAndFeatures carries ARP entries, notably a ProductCode for
	// non-MSI installers, used to strengthen detection.
	AppsAndFeatures []AppEntry `json:"apps_and_features,omitempty"`
}

// AppEntry mirrors a winget AppsAndFeaturesEntries element.
type AppEntry struct {
	ProductCode    string `json:"product_code,omitempty"`
	UpgradeCode    string `json:"upgrade_code,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	DisplayVersion string `json:"display_version,omitempty"`
}

// Manifest is one resolved package version.
type Manifest struct {
	PackageIdentifier string      `json:"package_identifier"`
	Version           string      `json:"version"`
	Name              string      `json:"name"`
	Publisher         string      `json:"publisher"`
	Description       string      `json:"description,omitempty"`
	Installers        []Installer `json:"installers"`
	// Dependencies is the flat list of PackageDependencies identifiers
	// (e.g. a VC++ redist) declared by this version.
	Dependencies []string `json:"dependencies,omitempty"`
}

// Searcher is the winget-source surface the API handlers depend on; tests
// swap in a fake so no test touches the network.
type Searcher interface {
	Search(ctx context.Context, query string) ([]Result, error)
	Manifest(ctx context.Context, packageID, version string) (*Manifest, error)
}

// Client resolves winget packages from the pre-indexed source and the
// winget-pkgs manifests.
type Client struct {
	HTTP *http.Client
	// IndexBaseURL serves the source index MSIX (overridable for tests).
	IndexBaseURL string
	// RawBaseURL is the manifest content root (overridable for tests).
	RawBaseURL string
	// CacheDir is where the extracted index database is cached between
	// requests. Empty falls back to the OS temp dir.
	CacheDir string
	// IndexTTL bounds how long a cached index is reused.
	IndexTTL time.Duration

	// idx guards the lazily-downloaded index cache.
	idx sync.Mutex
	// idxFetchedAt is when the cached index db was last (re)built.
	idxFetchedAt time.Time
	// idxPath is the on-disk path of the cached index db, once fetched.
	idxPath string
}

// New returns a client with sane defaults. cacheDir is where the source
// index database is cached; pass a per-server directory (e.g.
// <data>/winget). The default transport honours HTTPS_PROXY and friends.
func New(cacheDir string) *Client {
	return &Client{
		HTTP:         &http.Client{Timeout: 30 * time.Minute},
		IndexBaseURL: DefaultIndexBaseURL,
		RawBaseURL:   DefaultRawBaseURL,
		CacheDir:     cacheDir,
		IndexTTL:     DefaultIndexTTL,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) indexBaseURL() string {
	if c.IndexBaseURL != "" {
		return strings.TrimRight(c.IndexBaseURL, "/")
	}
	return DefaultIndexBaseURL
}

func (c *Client) rawBaseURL() string {
	if c.RawBaseURL != "" {
		return strings.TrimRight(c.RawBaseURL, "/")
	}
	return DefaultRawBaseURL
}

func (c *Client) indexTTL() time.Duration {
	if c.IndexTTL > 0 {
		return c.IndexTTL
	}
	return DefaultIndexTTL
}

// isNewerVersion reports whether a is a newer version string than b, by a
// dotted-numeric compare with a lexical fallback for non-numeric segments.
// b == "" is always older. Best-effort ordering to pick a sensible
// "latest"; the operator can still choose a specific version.
func isNewerVersion(a, b string) bool {
	if b == "" {
		return a != ""
	}
	if a == b {
		return false
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an > bn
			}
			continue
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

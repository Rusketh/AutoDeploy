// Package wingetsrc talks to the winget community REST source
// (cdn.winget.microsoft.com) so operators can import an application —
// installer payload plus a ready-made detection/install spec — straight
// from winget, then keep it as a standard, editable SoftwarePackage.
//
// This exists because many sites disable the Microsoft Store / winget
// source by policy, which stops the agent's live "winget install" step
// from ever reaching a source. Importing here downloads the real vendor
// installer to the server up front and builds a package that installs
// with plain msi/exe/appx steps — no Store or winget source required on
// the target at install time.
//
// Scope is deliberately the winget *community* source only: true
// Store-exclusive (msstore) apps are DRM/licence-locked and cannot be
// repackaged offline. Everything here is operator-driven; nothing
// searches or downloads on a schedule.
package wingetsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the winget community REST source the desktop client
// ships with as its default "winget" source.
const DefaultBaseURL = "https://cdn.winget.microsoft.com/cache"

// Result is one row of a manifestSearch response, trimmed to what the
// portal shows and what an import needs to resolve the full manifest.
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

// Searcher is the winget-source surface the API handlers depend on;
// tests swap in a fake so no test touches the network.
type Searcher interface {
	Search(ctx context.Context, query string) ([]Result, error)
	Manifest(ctx context.Context, packageID, version string) (*Manifest, error)
}

// Client talks to a winget REST source over HTTP.
type Client struct {
	HTTP    *http.Client
	BaseURL string // overridable for tests
}

// New returns a client pointed at the community source with sane
// defaults. The default transport honours HTTPS_PROXY and friends.
func New() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		BaseURL: DefaultBaseURL,
	}
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

// restHeaders applies the headers the REST source expects. The source
// requires an API version header on its endpoints.
func restHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoDeploy)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Version", "1.1.0")
}

// --- Search ---------------------------------------------------------------

type searchRequest struct {
	Query          searchQuery `json:"Query"`
	MaximumResults int         `json:"MaximumResults"`
}

type searchQuery struct {
	KeyWord   string `json:"KeyWord"`
	MatchType string `json:"MatchType"`
}

type searchResponse struct {
	Data []struct {
		PackageIdentifier string `json:"PackageIdentifier"`
		PackageName       string `json:"PackageName"`
		Publisher         string `json:"Publisher"`
		Versions          []struct {
			PackageVersion string `json:"PackageVersion"`
		} `json:"Versions"`
	} `json:"Data"`
}

// Search runs a keyword query against the source's manifestSearch
// endpoint and returns up to 25 matches. A query the source knows but
// has no matches for returns an empty slice and no error.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	body, err := json.Marshal(searchRequest{
		Query:          searchQuery{KeyWord: query, MatchType: "Substring"},
		MaximumResults: 25,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL()+"/api/manifestSearch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	restHeaders(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("winget search: %w", err)
	}
	defer resp.Body.Close()
	// A source with zero matches answers 204 No Content.
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("winget search: unexpected status %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("winget search: %w", err)
	}
	return parseSearch(raw)
}

func parseSearch(raw []byte) ([]Result, error) {
	var sr searchResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("winget search: %w", err)
	}
	out := make([]Result, 0, len(sr.Data))
	for _, d := range sr.Data {
		if d.PackageIdentifier == "" {
			continue
		}
		r := Result{
			PackageIdentifier: d.PackageIdentifier,
			Name:              d.PackageName,
			Publisher:         d.Publisher,
		}
		for _, v := range d.Versions {
			if isNewerVersion(v.PackageVersion, r.LatestVersion) {
				r.LatestVersion = v.PackageVersion
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// --- Manifest -------------------------------------------------------------

type manifestResponse struct {
	Data struct {
		PackageIdentifier string `json:"PackageIdentifier"`
		Versions          []struct {
			PackageVersion string `json:"PackageVersion"`
			DefaultLocale  struct {
				PackageName      string `json:"PackageName"`
				Publisher        string `json:"Publisher"`
				ShortDescription string `json:"ShortDescription"`
			} `json:"DefaultLocale"`
			// Version-level defaults some manifests set once and let
			// installers inherit.
			InstallerType string              `json:"InstallerType"`
			Scope         string              `json:"Scope"`
			Installers    []manifestInstaller `json:"Installers"`
			Dependencies  struct {
				PackageDependencies []struct {
					PackageIdentifier string `json:"PackageIdentifier"`
				} `json:"PackageDependencies"`
			} `json:"Dependencies"`
		} `json:"Versions"`
	} `json:"Data"`
}

type manifestInstaller struct {
	Architecture      string `json:"Architecture"`
	InstallerType     string `json:"InstallerType"`
	Scope             string `json:"Scope"`
	InstallerURL      string `json:"InstallerUrl"`
	InstallerSha256   string `json:"InstallerSha256"`
	ProductCode       string `json:"ProductCode"`
	InstallerSwitches struct {
		Silent             string `json:"Silent"`
		SilentWithProgress string `json:"SilentWithProgress"`
		Custom             string `json:"Custom"`
	} `json:"InstallerSwitches"`
	AppsAndFeaturesEntries []struct {
		ProductCode    string `json:"ProductCode"`
		UpgradeCode    string `json:"UpgradeCode"`
		DisplayName    string `json:"DisplayName"`
		DisplayVersion string `json:"DisplayVersion"`
	} `json:"AppsAndFeaturesEntries"`
}

// Manifest resolves a package's full manifest. When version is empty the
// latest version present in the response is chosen.
func (c *Client) Manifest(ctx context.Context, packageID, version string) (*Manifest, error) {
	if strings.TrimSpace(packageID) == "" {
		return nil, fmt.Errorf("winget manifest: package id required")
	}
	u := c.baseURL() + "/api/packageManifests/" + url.PathEscape(packageID)
	if v := strings.TrimSpace(version); v != "" {
		u += "?Version=" + url.QueryEscape(v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	restHeaders(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("winget manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("winget manifest: package %q not found", packageID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("winget manifest: unexpected status %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("winget manifest: %w", err)
	}
	return parseManifest(raw, version)
}

func parseManifest(raw []byte, wantVersion string) (*Manifest, error) {
	var mr manifestResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("winget manifest: %w", err)
	}
	if len(mr.Data.Versions) == 0 {
		return nil, fmt.Errorf("winget manifest: no versions in response")
	}
	// Pick the requested version, else the newest one present.
	idx := -1
	for i, v := range mr.Data.Versions {
		if wantVersion != "" {
			if v.PackageVersion == wantVersion {
				idx = i
				break
			}
			continue
		}
		if idx < 0 || isNewerVersion(v.PackageVersion, mr.Data.Versions[idx].PackageVersion) {
			idx = i
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("winget manifest: version %q not found", wantVersion)
	}
	v := mr.Data.Versions[idx]

	out := &Manifest{
		PackageIdentifier: mr.Data.PackageIdentifier,
		Version:           v.PackageVersion,
		Name:              v.DefaultLocale.PackageName,
		Publisher:         v.DefaultLocale.Publisher,
		Description:       v.DefaultLocale.ShortDescription,
	}
	for _, mi := range v.Installers {
		it := mi.InstallerType
		if it == "" {
			it = v.InstallerType // inherit version-level default
		}
		sc := mi.Scope
		if sc == "" {
			sc = v.Scope
		}
		inst := Installer{
			Architecture:       strings.ToLower(mi.Architecture),
			InstallerType:      strings.ToLower(it),
			Scope:              strings.ToLower(sc),
			URL:                mi.InstallerURL,
			SHA256:             strings.ToLower(mi.InstallerSha256),
			ProductCode:        mi.ProductCode,
			Silent:             mi.InstallerSwitches.Silent,
			SilentWithProgress: mi.InstallerSwitches.SilentWithProgress,
			Custom:             mi.InstallerSwitches.Custom,
		}
		for _, ae := range mi.AppsAndFeaturesEntries {
			inst.AppsAndFeatures = append(inst.AppsAndFeatures, AppEntry{
				ProductCode:    ae.ProductCode,
				UpgradeCode:    ae.UpgradeCode,
				DisplayName:    ae.DisplayName,
				DisplayVersion: ae.DisplayVersion,
			})
		}
		out.Installers = append(out.Installers, inst)
	}
	for _, d := range v.Dependencies.PackageDependencies {
		if d.PackageIdentifier != "" {
			out.Dependencies = append(out.Dependencies, d.PackageIdentifier)
		}
	}
	return out, nil
}

// isNewerVersion reports whether a is a newer version string than b, by a
// dotted-numeric compare with a lexical fallback for non-numeric
// segments. b == "" is always older. This is best-effort ordering to pick
// a sensible "latest"; the operator can still choose a specific version.
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

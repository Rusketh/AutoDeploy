package wingetsrc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
)

// Search resolves keyword matches from the pre-indexed source. A query the
// catalog knows nothing about returns an empty slice and no error.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	dbPath, err := c.ensureIndex(ctx)
	if err != nil {
		return nil, err
	}
	return c.searchIndex(ctx, dbPath, query, 25)
}

// Manifest resolves a package version's full manifest. When version is
// empty the newest version in the index is used. The version's manifest
// path is derived deterministically from the identifier (winget-pkgs
// stores every manifest at manifests/<c>/<id parts>/<version>/, where <c>
// is the lowercased first character of the identifier), so the index is
// needed only to discover which versions exist — not to walk paths.
func (c *Client) Manifest(ctx context.Context, packageID, version string) (*Manifest, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return nil, fmt.Errorf("winget manifest: package id required")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		dbPath, err := c.ensureIndex(ctx)
		if err != nil {
			return nil, err
		}
		vers, err := c.indexVersions(ctx, dbPath, packageID)
		if err != nil {
			return nil, err
		}
		if len(vers) == 0 {
			return nil, fmt.Errorf("winget manifest: package %q not found in catalog", packageID)
		}
		version = vers[0] // newest first
	}

	base := c.rawBaseURL() + "/" + manifestDir(packageID, version)

	installerYAML, err := c.fetchManifestFile(ctx, base, packageID+".installer.yaml")
	if err != nil {
		return nil, err
	}
	m, err := parseInstallerYAML(installerYAML)
	if err != nil {
		return nil, fmt.Errorf("winget manifest %s %s: %w", packageID, version, err)
	}
	m.PackageIdentifier = packageID
	if m.Version == "" {
		m.Version = version
	}

	// The version manifest names the default locale; the locale manifest
	// carries the display name / publisher / description. Both are
	// best-effort — a package still imports without them.
	if verYAML, err := c.fetchManifestFile(ctx, base, packageID+".yaml"); err == nil {
		locale := parseDefaultLocale(verYAML)
		if locale != "" {
			if locYAML, err := c.fetchManifestFile(ctx, base,
				packageID+".locale."+locale+".yaml"); err == nil {
				applyLocale(m, locYAML)
			}
		}
	}
	return m, nil
}

// manifestDir builds the winget-pkgs manifest directory for an identifier
// and version: manifests/<lowered first char>/<id parts joined by '/'>/<version>.
// e.g. ("7zip.7zip","23.01") -> "manifests/7/7zip/7zip/23.01" and
// ("Microsoft.VCRedist.2015+.x64","14.0") ->
// "manifests/m/Microsoft/VCRedist/2015+/x64/14.0".
func manifestDir(id, version string) string {
	first := "_"
	if id != "" {
		first = strings.ToLower(id[:1])
	}
	parts := strings.Split(id, ".")
	return "manifests/" + first + "/" + strings.Join(parts, "/") + "/" + version
}

// fetchManifestFile GETs one manifest YAML file under base.
func (c *Client) fetchManifestFile(ctx context.Context, base, name string) ([]byte, error) {
	u := base + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "autodeploy-server")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// --- YAML shapes (winget manifest schema 1.x) -----------------------------

type installerManifestYAML struct {
	PackageIdentifier string `yaml:"PackageIdentifier"`
	PackageVersion    string `yaml:"PackageVersion"`
	// Version-level defaults that installers inherit when unset.
	InstallerType     string                `yaml:"InstallerType"`
	Scope             string                `yaml:"Scope"`
	InstallerSwitches installerSwitchesYAML `yaml:"InstallerSwitches"`
	ProductCode       string                `yaml:"ProductCode"`
	Installers        []installerEntryYAML  `yaml:"Installers"`
	Dependencies      dependenciesYAML      `yaml:"Dependencies"`
}

type installerEntryYAML struct {
	Architecture           string                `yaml:"Architecture"`
	InstallerType          string                `yaml:"InstallerType"`
	Scope                  string                `yaml:"Scope"`
	InstallerURL           string                `yaml:"InstallerUrl"`
	InstallerSha256        string                `yaml:"InstallerSha256"`
	ProductCode            string                `yaml:"ProductCode"`
	InstallerSwitches      installerSwitchesYAML `yaml:"InstallerSwitches"`
	AppsAndFeaturesEntries []appsFeaturesYAML    `yaml:"AppsAndFeaturesEntries"`
	Dependencies           dependenciesYAML      `yaml:"Dependencies"`
}

type installerSwitchesYAML struct {
	Silent             string `yaml:"Silent"`
	SilentWithProgress string `yaml:"SilentWithProgress"`
	Custom             string `yaml:"Custom"`
}

type appsFeaturesYAML struct {
	ProductCode    string `yaml:"ProductCode"`
	UpgradeCode    string `yaml:"UpgradeCode"`
	DisplayName    string `yaml:"DisplayName"`
	DisplayVersion string `yaml:"DisplayVersion"`
}

type dependenciesYAML struct {
	PackageDependencies []struct {
		PackageIdentifier string `yaml:"PackageIdentifier"`
	} `yaml:"PackageDependencies"`
}

// parseInstallerYAML maps an installer manifest into our Manifest,
// applying version-level → installer-level inheritance for type, scope and
// switches.
func parseInstallerYAML(raw []byte) (*Manifest, error) {
	var y installerManifestYAML
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return nil, fmt.Errorf("parse installer.yaml: %w", err)
	}
	m := &Manifest{
		PackageIdentifier: y.PackageIdentifier,
		Version:           y.PackageVersion,
	}
	seenDep := map[string]bool{}
	addDeps := func(d dependenciesYAML) {
		for _, pd := range d.PackageDependencies {
			id := strings.TrimSpace(pd.PackageIdentifier)
			if id != "" && !seenDep[id] {
				seenDep[id] = true
				m.Dependencies = append(m.Dependencies, id)
			}
		}
	}
	addDeps(y.Dependencies)

	for _, ie := range y.Installers {
		it := firstNonEmpty(ie.InstallerType, y.InstallerType)
		sc := firstNonEmpty(ie.Scope, y.Scope)
		sw := ie.InstallerSwitches
		if sw.Silent == "" {
			sw.Silent = y.InstallerSwitches.Silent
		}
		if sw.SilentWithProgress == "" {
			sw.SilentWithProgress = y.InstallerSwitches.SilentWithProgress
		}
		if sw.Custom == "" {
			sw.Custom = y.InstallerSwitches.Custom
		}
		inst := Installer{
			Architecture:       strings.ToLower(strings.TrimSpace(ie.Architecture)),
			InstallerType:      strings.ToLower(strings.TrimSpace(it)),
			Scope:              strings.ToLower(strings.TrimSpace(sc)),
			URL:                strings.TrimSpace(ie.InstallerURL),
			SHA256:             strings.ToLower(strings.TrimSpace(ie.InstallerSha256)),
			ProductCode:        firstNonEmpty(ie.ProductCode, y.ProductCode),
			Silent:             sw.Silent,
			SilentWithProgress: sw.SilentWithProgress,
			Custom:             sw.Custom,
		}
		for _, ae := range ie.AppsAndFeaturesEntries {
			inst.AppsAndFeatures = append(inst.AppsAndFeatures, AppEntry{
				ProductCode:    ae.ProductCode,
				UpgradeCode:    ae.UpgradeCode,
				DisplayName:    ae.DisplayName,
				DisplayVersion: ae.DisplayVersion,
			})
		}
		addDeps(ie.Dependencies)
		m.Installers = append(m.Installers, inst)
	}
	if len(m.Installers) == 0 {
		return nil, fmt.Errorf("installer.yaml has no installers")
	}
	return m, nil
}

// parseDefaultLocale extracts the DefaultLocale field from a version
// manifest; empty when absent.
func parseDefaultLocale(raw []byte) string {
	var y struct {
		DefaultLocale string `yaml:"DefaultLocale"`
	}
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return ""
	}
	return strings.TrimSpace(y.DefaultLocale)
}

// applyLocale fills name/publisher/description on m from a locale manifest.
func applyLocale(m *Manifest, raw []byte) {
	var y struct {
		PackageName      string `yaml:"PackageName"`
		Publisher        string `yaml:"Publisher"`
		ShortDescription string `yaml:"ShortDescription"`
	}
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return
	}
	if y.PackageName != "" {
		m.Name = y.PackageName
	}
	if y.Publisher != "" {
		m.Publisher = y.Publisher
	}
	if y.ShortDescription != "" {
		m.Description = y.ShortDescription
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

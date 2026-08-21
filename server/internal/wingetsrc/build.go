package wingetsrc

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// DownloadFile is one installer the importer must fetch into the
// package's files/ directory before the generated steps can run.
type DownloadFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256,omitempty"`
}

// BuiltPackage is the fully-formed, ready-to-persist shape of an imported
// winget app: a suggested name/description, the detection rules and
// ordered install steps (prerequisites first), and the installer files to
// download. It is deliberately identical in shape to what an operator
// would build by hand, so the result is a standard editable package.
type BuiltPackage struct {
	Name        string
	Description string
	Detection   []swspec.DetectionRule
	Steps       []swspec.InstallStep
	Files       []DownloadFile
}

// DepInstaller pairs a resolved prerequisite manifest with the installer
// variant chosen for it, so BuildPackage can bundle prereqs into the same
// package with their steps ordered before the main app's step.
type DepInstaller struct {
	Manifest  *Manifest
	Installer *Installer
}

// PickInstaller chooses the best installer variant for the requested
// architecture and scope, falling back gracefully. Empty arch/scope mean
// "the sensible default" (x64, machine). Returns nil when the list is
// empty. Preference order: exact arch+scope, exact arch (any scope),
// preferred-arch fallbacks (x64→x86→arm64→neutral), then the first
// installer so a single-variant package always resolves.
func PickInstaller(installers []Installer, arch, scope string) *Installer {
	if len(installers) == 0 {
		return nil
	}
	arch = strings.ToLower(strings.TrimSpace(arch))
	scope = strings.ToLower(strings.TrimSpace(scope))
	if arch == "" {
		arch = "x64"
	}
	if scope == "" {
		scope = "machine"
	}

	// Exact arch + exact scope.
	if inst := findInstaller(installers, arch, scope); inst != nil {
		return inst
	}
	// Exact arch, any scope.
	if inst := findInstaller(installers, arch, ""); inst != nil {
		return inst
	}
	// Fallback architectures, preserving the requested scope first then any.
	for _, a := range archFallbacks(arch) {
		if inst := findInstaller(installers, a, scope); inst != nil {
			return inst
		}
		if inst := findInstaller(installers, a, ""); inst != nil {
			return inst
		}
	}
	// Last resort: the first installer offered.
	return &installers[0]
}

func findInstaller(installers []Installer, arch, scope string) *Installer {
	for i := range installers {
		if installers[i].Architecture != arch {
			continue
		}
		if scope != "" && installers[i].Scope != scope {
			continue
		}
		return &installers[i]
	}
	return nil
}

// archFallbacks returns alternate architectures to try, best first, for a
// requested architecture that has no exact match.
func archFallbacks(arch string) []string {
	switch arch {
	case "x64":
		return []string{"x86", "arm64", "neutral"}
	case "x86":
		return []string{"x64", "arm64", "neutral"}
	case "arm64":
		return []string{"x64", "x86", "neutral"}
	default:
		return []string{"x64", "x86", "arm64", "neutral"}
	}
}

// BuildPackage turns a resolved manifest + chosen installer (plus any
// resolved prerequisites) into a standard package spec: files to
// download, ordered install steps (prereqs first), and detection rules.
// It never fails on a prerequisite that couldn't be resolved — such
// prereqs are named in the description as a heads-up and simply left out,
// so the main app still imports.
func BuildPackage(m *Manifest, chosen *Installer, deps []DepInstaller, unresolvedDeps []string) (BuiltPackage, error) {
	if m == nil || chosen == nil {
		return BuiltPackage{}, fmt.Errorf("winget build: manifest and installer required")
	}

	name := m.Name
	if name == "" {
		name = m.PackageIdentifier
	}
	if m.Version != "" {
		name = fmt.Sprintf("%s %s", name, m.Version)
	}

	var descParts []string
	if m.Publisher != "" {
		descParts = append(descParts, m.Publisher)
	}
	if m.Description != "" {
		descParts = append(descParts, m.Description)
	}
	descParts = append(descParts, fmt.Sprintf("Imported from winget (%s).", m.PackageIdentifier))

	out := BuiltPackage{}
	usedNames := map[string]bool{}

	// Prerequisites first, in declared order.
	for _, d := range deps {
		if d.Manifest == nil || d.Installer == nil {
			continue
		}
		fname := uniqueFilename(installerFilename(d.Installer.URL, d.Manifest, d.Installer), usedNames)
		out.Files = append(out.Files, DownloadFile{
			URL: d.Installer.URL, Filename: fname, SHA256: d.Installer.SHA256,
		})
		if step, ok := installerStep(d.Installer, fname,
			fmt.Sprintf("Prerequisite: %s %s", depDisplayName(d.Manifest), d.Manifest.Version)); ok {
			out.Steps = append(out.Steps, step)
		}
	}

	// The main application.
	fname := uniqueFilename(installerFilename(chosen.URL, m, chosen), usedNames)
	out.Files = append(out.Files, DownloadFile{
		URL: chosen.URL, Filename: fname, SHA256: chosen.SHA256,
	})
	mainStep, ok := installerStep(chosen, fname, fmt.Sprintf("Install %s", name))
	if ok {
		out.Steps = append(out.Steps, mainStep)
	} else {
		descParts = append(descParts,
			fmt.Sprintf("NOTE: installer type %q has no automatic step — %s was downloaded; add an install step by hand.",
				chosen.InstallerType, fname))
	}

	if len(unresolvedDeps) > 0 {
		descParts = append(descParts,
			"NOTE: could not resolve these declared prerequisites (add them manually): "+
				strings.Join(unresolvedDeps, ", ")+".")
	}

	out.Name = name
	out.Description = strings.Join(descParts, " ")
	out.Detection = buildDetection(m, chosen)
	return out, nil
}

// depDisplayName is the human name for a prerequisite manifest.
func depDisplayName(m *Manifest) string {
	if m.Name != "" {
		return m.Name
	}
	return m.PackageIdentifier
}

// installerStep maps a winget installer type to the equivalent
// swspec.InstallStep. The bool is false for types with no automatic
// mapping (e.g. zip / unknown), which the caller handles by leaving the
// file for the operator to wire up.
func installerStep(inst *Installer, filename, desc string) (swspec.InstallStep, bool) {
	// 0 and 3010 (reboot required) are both success for installers.
	base := swspec.InstallStep{Description: desc, SuccessCodes: []int{0, 3010}}
	switch inst.InstallerType {
	case "msi", "wix":
		base.Type = "msi"
		base.MSIPath = filename
		base.MSIArgs = extraMSIArgs(inst)
		return base, true
	case "msix", "appx":
		base.Type = "appx"
		base.APPXPath = filename
		return base, true
	case "exe", "inno", "nullsoft", "burn", "portable":
		base.Type = "exe"
		base.ExePath = filename
		base.ExeArgs = silentExeArgs(inst)
		return base, true
	default:
		return swspec.InstallStep{}, false
	}
}

// extraMSIArgs pulls any custom switches from the manifest to append
// after the agent's own "/quiet /norestart". winget MSI silent switches
// are usually already implied by msiexec's quiet mode, so only Custom is
// forwarded (as whitespace-split tokens).
func extraMSIArgs(inst *Installer) []string {
	if s := strings.TrimSpace(inst.Custom); s != "" {
		return strings.Fields(s)
	}
	return nil
}

// silentExeArgs picks the quiet-install arguments for an exe-family
// installer: the manifest's Silent switch when present, else a sensible
// per-type default.
func silentExeArgs(inst *Installer) []string {
	if s := strings.TrimSpace(inst.Silent); s != "" {
		return strings.Fields(s)
	}
	if s := strings.TrimSpace(inst.SilentWithProgress); s != "" {
		return strings.Fields(s)
	}
	switch inst.InstallerType {
	case "inno":
		return []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}
	case "nullsoft":
		return []string{"/S"}
	case "burn":
		return []string{"/quiet", "/norestart"}
	default:
		return nil
	}
}

// buildDetection builds detection rules for an imported app. A winget
// rule is always included (the agent's `winget list` works with the local
// client even when the Store source is policy-disabled). When the main
// installer carries a product code we add an msi rule too, so detection
// still works on a target with no winget at all.
func buildDetection(m *Manifest, chosen *Installer) []swspec.DetectionRule {
	rules := []swspec.DetectionRule{
		{Type: "winget", WingetID: m.PackageIdentifier},
	}
	if pc := productCode(chosen); pc != "" {
		rules = append(rules, swspec.DetectionRule{Type: "msi", MSIProductCode: pc})
	}
	return rules
}

// productCode returns a usable MSI product code for the installer: the
// installer's own ProductCode, else the first AppsAndFeatures entry's.
// Only braced GUIDs are returned, since that is what msi detection
// expects.
func productCode(inst *Installer) string {
	if c := strings.TrimSpace(inst.ProductCode); isBracedGUID(c) {
		return c
	}
	for _, ae := range inst.AppsAndFeatures {
		if c := strings.TrimSpace(ae.ProductCode); isBracedGUID(c) {
			return c
		}
	}
	return ""
}

func isBracedGUID(s string) bool {
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && len(s) >= 10
}

// installerFilename derives a safe on-disk filename for an installer,
// falling back to "<id>-<version>.<ext>" when the URL has no usable base
// name (winget InstallerUrls sometimes end in a query or a redirect path).
func installerFilename(installerURL string, m *Manifest, inst *Installer) string {
	if u, err := url.Parse(installerURL); err == nil {
		base := path.Base(u.Path)
		if base != "" && base != "." && base != "/" &&
			!strings.ContainsAny(base, `\`) && !strings.Contains(base, "..") &&
			strings.Contains(base, ".") {
			return base
		}
	}
	// Synthesize one from the identity + a type-appropriate extension.
	id := strings.ReplaceAll(m.PackageIdentifier, ".", "-")
	ver := m.Version
	name := id
	if ver != "" {
		name += "-" + ver
	}
	return sanitizeSegment(name) + extForType(inst.InstallerType)
}

func extForType(t string) string {
	switch t {
	case "msi", "wix":
		return ".msi"
	case "msix":
		return ".msix"
	case "appx":
		return ".appx"
	case "zip":
		return ".zip"
	default:
		return ".exe"
	}
}

// sanitizeSegment reduces a synthesized name to filename-safe characters.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "installer"
	}
	return out
}

// uniqueFilename disambiguates a filename against those already used in
// the package by inserting a numeric suffix before the extension.
func uniqueFilename(name string, used map[string]bool) string {
	if !used[strings.ToLower(name)] {
		used[strings.ToLower(name)] = true
		return name
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !used[strings.ToLower(cand)] {
			used[strings.ToLower(cand)] = true
			return cand
		}
	}
}

// Package resolve computes a machine's effective configuration given a
// selected image and (in later phases) hardware identity. The rules are
// stated in docs/design/AutoDeploy_Design_Document.docx §4.2 and the
// project context primer §5:
//
//   - ISO          nearest-wins up the image's parent chain.
//   - Unattend     nearest-wins, used in full (NOT merged).
//   - Drivers      every package whose SMBIOS filters match (Phase 4).
//   - Software     additive union of directly linked packages and loadout
//                  packages (Phase 7), de-duplicated by package ID, with
//                  direct-link ordering taking precedence.
//
// Phase 1 ships ISO+unattend nearest-wins resolution and a deterministic
// software-link union over direct image links. Loadouts and driver matching
// arrive in their respective phases.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// Resolved is the machine-ready manifest a Boot Client would receive.
type Resolved struct {
	ImageID    model.ID                  `json:"image_id"`
	ISO        *model.ISO                `json:"iso,omitempty"`
	Unattend   *model.Unattend           `json:"unattend,omitempty"`
	Software   []model.ImageSoftwareLink `json:"software"`
	// Drivers is populated only by ResolveForMachine; the no-identity
	// Resolve returns it empty because driver matching needs reported
	// hardware.
	Drivers    []model.DriverPackage `json:"drivers,omitempty"`
	ChainNames []string              `json:"chain_names"`
	// Diagnostics surfaces issues that should be visible in the portal,
	// not raised as errors: a missing ISO, a missing unattend, an empty
	// software set, etc. The resolver does not refuse to produce a manifest
	// in these cases; it is the portal's job to warn.
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// driverCacheTTL is how long the cached driver list is considered fresh.
const driverCacheTTL = 30 * time.Second

// driverCache holds a time-limited snapshot of the driver package list so
// ResolveForMachine does not hit the database on every manifest request.
type driverCache struct {
	mu      sync.Mutex
	pkgs    []model.DriverPackage
	fetched time.Time
}

// Resolver walks image chains. It is constructed once and reused.
type Resolver struct {
	images   *model.ImageRepo
	isos     *model.ISORepo
	unattend *model.UnattendRepo
	drivers  *model.DriverPackageRepo     // nil = driver matching disabled
	loadouts *model.SoftwareLoadoutRepo   // nil = loadout resolution disabled
	dc       driverCache
}

// New constructs a resolver bound to the given repositories. The drivers
// repo is optional and may be wired in via WithDrivers; this keeps the
// Phase 1/2 callers that did not yet know about driver matching working.
func New(images *model.ImageRepo, isos *model.ISORepo, unattend *model.UnattendRepo) *Resolver {
	return &Resolver{images: images, isos: isos, unattend: unattend}
}

// WithDrivers attaches the driver-package repo so ResolveForMachine can
// evaluate driver filters. Returns r for chaining.
func (r *Resolver) WithDrivers(drivers *model.DriverPackageRepo) *Resolver {
	r.drivers = drivers
	return r
}

// WithLoadouts attaches the software-loadout repo so the resolver can
// expand an image's loadout link into the inherited software set. Returns
// r for chaining.
func (r *Resolver) WithLoadouts(loadouts *model.SoftwareLoadoutRepo) *Resolver {
	r.loadouts = loadouts
	return r
}

// InvalidateDriverCache forces the next ResolveForMachine call to re-read
// the driver list from the database. Call this after creating, updating, or
// deleting a driver package.
func (r *Resolver) InvalidateDriverCache() {
	r.dc.mu.Lock()
	r.dc.pkgs = nil
	r.dc.fetched = time.Time{}
	r.dc.mu.Unlock()
}

// cachedDrivers returns the driver list, using a time-limited cache to avoid
// hitting the database on every manifest request.
func (r *Resolver) cachedDrivers(ctx context.Context) ([]model.DriverPackage, error) {
	r.dc.mu.Lock()
	if r.dc.pkgs != nil && time.Since(r.dc.fetched) < driverCacheTTL {
		cached := r.dc.pkgs
		r.dc.mu.Unlock()
		return cached, nil
	}
	r.dc.mu.Unlock()

	pkgs, err := r.drivers.List(ctx)
	if err != nil {
		return nil, err
	}

	r.dc.mu.Lock()
	r.dc.pkgs = pkgs
	r.dc.fetched = time.Now()
	r.dc.mu.Unlock()
	return pkgs, nil
}

// Resolve computes the effective manifest for the image with id.
func (r *Resolver) Resolve(ctx context.Context, id model.ID) (Resolved, error) {
	chain, err := r.chain(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{ImageID: id}
	for _, im := range chain {
		out.ChainNames = append(out.ChainNames, im.Name)
	}

	// Nearest-wins: walk from the selected image upward, take the first hit.
	for _, im := range chain {
		if im.ISOID != nil && out.ISO == nil {
			iso, err := r.isos.Get(ctx, *im.ISOID)
			if err != nil {
				return Resolved{}, fmt.Errorf("load iso %d for image %d: %w", *im.ISOID, im.ID, err)
			}
			out.ISO = &iso
		}
		if im.UnattendID != nil && out.Unattend == nil {
			ua, err := r.unattend.Get(ctx, *im.UnattendID)
			if err != nil {
				return Resolved{}, fmt.Errorf("load unattend %d for image %d: %w", *im.UnattendID, im.ID, err)
			}
			out.Unattend = &ua
		}
	}

	// Software: direct image links across the chain take precedence, then
	// loadout-derived packages fill in. Dedupe is first-seen by package ID.
	seen := map[model.ID]bool{}
	for _, im := range chain {
		for _, l := range im.SoftwareLinks {
			if seen[l.PackageID] {
				continue
			}
			seen[l.PackageID] = true
			out.Software = append(out.Software, l)
		}
	}
	// Loadout-derived software (Phase 7). The nearest-wins loadout on the
	// image chain owns the loadout source; ancestors do not contribute
	// their own loadouts. That matches the "nearest-wins" pattern used by
	// ISO and unattend, while the package set inside the loadout is still
	// additive via the loadout's own parent chain.
	if r.loadouts != nil {
		var loadoutID *model.ID
		for _, im := range chain {
			if im.LoadoutID != nil {
				loadoutID = im.LoadoutID
				break
			}
		}
		if loadoutID != nil {
			pkgs, err := r.resolveLoadout(ctx, *loadoutID)
			if err != nil {
				return Resolved{}, fmt.Errorf("resolve loadout %d: %w", *loadoutID, err)
			}
			for _, p := range pkgs {
				if seen[p.PackageID] {
					continue
				}
				seen[p.PackageID] = true
				out.Software = append(out.Software, model.ImageSoftwareLink{
					PackageID:  p.PackageID,
					OrderValue: p.OrderValue,
				})
			}
		}
	}

	if out.ISO == nil {
		out.Diagnostics = append(out.Diagnostics,
			"no ISO resolved up the inheritance chain — a machine cannot be deployed from this image")
	}
	if out.Unattend == nil {
		out.Diagnostics = append(out.Diagnostics,
			"no unattend resolved up the inheritance chain — deployment will not be unattended")
	}
	return out, nil
}

// ResolveForMachine is the per-machine resolution path. It runs Resolve to
// compute the image-derived configuration, then evaluates every driver
// package's filters against the reported identity and appends the matches.
//
// Driver matching is GLOBAL: image inheritance does not scope it. A package
// applies if ANY of its filters matches the identity.
func (r *Resolver) ResolveForMachine(ctx context.Context, id model.ID, identity match.Identity) (Resolved, error) {
	out, err := r.Resolve(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	if r.drivers == nil {
		return out, nil
	}
	pkgs, err := r.cachedDrivers(ctx)
	if err != nil {
		return Resolved{}, fmt.Errorf("load driver packages: %w", err)
	}
	for _, p := range pkgs {
		if packageMatches(p, identity) {
			out.Drivers = append(out.Drivers, p)
		}
	}
	if len(pkgs) > 0 && len(out.Drivers) == 0 {
		out.Diagnostics = append(out.Diagnostics,
			fmt.Sprintf("no driver packages matched the reported hardware (%d packages evaluated)",
				len(pkgs)))
	}
	return out, nil
}

// resolveLoadout walks the loadout's parent chain (eldest first) and
// accumulates packages. Child overrides parent: a same-package entry in a
// descendant replaces the ancestor's order_value, and OptOut removes the
// package from the resolved set.
func (r *Resolver) resolveLoadout(ctx context.Context, id model.ID) ([]model.SoftwareLoadoutPackage, error) {
	chain, err := r.loadoutChain(ctx, id)
	if err != nil {
		return nil, err
	}
	// Walk from eldest ancestor down so a descendant's entry overwrites
	// an ancestor's.
	merged := map[model.ID]model.SoftwareLoadoutPackage{}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, p := range chain[i].Packages {
			if p.OptOut {
				delete(merged, p.PackageID)
				continue
			}
			merged[p.PackageID] = p
		}
	}
	// Deterministic order: by OrderValue, then by PackageID.
	out := make([]model.SoftwareLoadoutPackage, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sortLoadoutPackages(out)
	return out, nil
}

func (r *Resolver) loadoutChain(ctx context.Context, id model.ID) ([]model.SoftwareLoadout, error) {
	const maxDepth = 256
	var out []model.SoftwareLoadout
	seen := map[model.ID]bool{}
	cur := id
	for depth := 0; depth < maxDepth; depth++ {
		if seen[cur] {
			return nil, errors.New("loadout inheritance cycle")
		}
		seen[cur] = true
		l, err := r.loadouts.Get(ctx, cur)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
		if l.ParentID == nil {
			return out, nil
		}
		cur = *l.ParentID
	}
	return nil, fmt.Errorf("loadout chain depth exceeded %d", maxDepth)
}

func sortLoadoutPackages(p []model.SoftwareLoadoutPackage) {
	// Insertion sort — expected N is small.
	for i := 1; i < len(p); i++ {
		for j := i; j > 0; j-- {
			a, b := p[j-1], p[j]
			if a.OrderValue < b.OrderValue ||
				(a.OrderValue == b.OrderValue && a.PackageID < b.PackageID) {
				break
			}
			p[j-1], p[j] = b, a
		}
	}
}

func packageMatches(p model.DriverPackage, id match.Identity) bool {
	for _, raw := range p.Filters {
		f, err := match.ParseFilter(raw.FilterJSON)
		if err != nil {
			// A persisted filter that fails to parse is a configuration
			// bug, not a runtime crash. Skip it; the portal validates at
			// save time so this should not happen in practice.
			continue
		}
		if f.Matches(id) {
			return true
		}
	}
	return false
}

// chain returns images from the selected one up to the root, inclusive.
// Detects and aborts on cycles in existing data.
func (r *Resolver) chain(ctx context.Context, id model.ID) ([]model.Image, error) {
	const maxDepth = 256
	var out []model.Image
	seen := map[model.ID]bool{}
	cur := id
	for depth := 0; depth < maxDepth; depth++ {
		if seen[cur] {
			return nil, errors.New("inheritance cycle detected while resolving")
		}
		seen[cur] = true
		im, err := r.images.Get(ctx, cur)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
		if im.ParentID == nil {
			return out, nil
		}
		cur = *im.ParentID
	}
	return nil, fmt.Errorf("chain depth exceeded %d", maxDepth)
}

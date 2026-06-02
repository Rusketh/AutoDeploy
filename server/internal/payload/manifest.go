package payload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/unattend"
)

// Manifest is the Boot Client's instruction sheet: a flat list of URLs to
// fetch, with each entry's role and intended on-disk path. The Boot Client
// is a fact reporter and a step executor — it does not compute any of this.
type Manifest struct {
	ImageID model.ID `json:"image_id"`
	BaseURL string   `json:"base_url"`
	// AgentID is the machine's server-minted object id. The Boot Client
	// writes it (with the server URL) into the registry at deploy time so
	// the resident agent can identify itself without trusting the BIOS
	// UUID. Empty on the identity-less GET path.
	AgentID  string         `json:"agent_id,omitempty"`
	Items    []ManifestItem `json:"items"`
	Warnings []string       `json:"warnings,omitempty"`
}

// ManifestItem is one downloadable payload.
type ManifestItem struct {
	Role string `json:"role"` // "iso-media", "driver", "software", "unattend"
	URL  string `json:"url"`  // absolute or root-relative
	// Base is set for "iso-media": the URL prefix the client joins with
	// each relative path from the media index (URL points at index.json).
	Base string `json:"base,omitempty"`
	Size int64  `json:"size_bytes,omitempty"`
	OS   string `json:"os_type,omitempty"`
	Name string `json:"name,omitempty"`
}

// ManifestHandler returns the resolver-backed manifest for a given image,
// fronted by HTTP. It is the endpoint the Boot Client calls after the
// operator picks a configuration.
type ManifestHandler struct {
	Resolver *resolve.Resolver
	// AD is the Domain Integration Service (Phase 10). When non-nil and
	// the resolved unattend has a domain_join section AND the machine has
	// a binding, the handler prepares the computer object before
	// returning the manifest so by the time the unattend runs JoinDomain
	// the object is ready.
	AD        *addomain.Service
	Inventory *model.InventoryRepo
	Unattend  *model.UnattendRepo
	// Software lets the handler check whether a resolved software package
	// actually has a servable payload before emitting a manifest item for
	// it. Without this a deleted/empty package would resolve into a URL
	// that 404s and aborts the Boot Client's deploy. Nil disables the
	// check (every resolved package is emitted unconditionally).
	Software *model.SoftwarePackageRepo
	// Mirrors lets the handler rewrite payload URLs to a site-local
	// mirror. Nil disables mirror routing.
	Mirrors *model.PayloadMirrorRepo
	// DomainJoin stores per-image agent-driven AD join config. When set,
	// prepareAD also fires for images that use agent-driven join (even
	// when the unattend itself has no legacy DomainJoin block).
	DomainJoin *model.DomainJoinRepo
}

// SiteHeader is the HTTP header the Boot Client / agent sets to tell
// the server which site it is in. Empty means unknown and falls back
// to a global mirror (or the primary if none is registered).
const SiteHeader = "X-AutoDeploy-Site"

// Handler returns an http.HandlerFunc that builds the manifest for the image
// id in the URL path. POST with identity in the JSON body so the resolver
// can match driver packages against the reported hardware. The base URL is
// derived from the request so the client uses whatever host:port reached
// us.
func (h *ManifestHandler) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var identity match.Identity
		// Accept GET (no identity → drivers skipped) or POST (identity →
		// drivers matched). The GET path keeps the portal's
		// "view manifest" link usable.
		if r.Method == http.MethodPost && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&identity); err != nil {
				http.Error(w, "invalid identity body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		// Site routing: prefer the header the Boot Client sends; fall
		// back to the machine record's last_site if we know the
		// machine; finally fall back to "" which uses a global mirror.
		site := strings.TrimSpace(r.Header.Get(SiteHeader))
		if site != "" && h.Mirrors != nil && identity.SystemUUID != "" {
			_ = h.Mirrors.RecordSiteForMachine(r.Context(), identity.SystemUUID, site)
		} else if site == "" && h.Mirrors != nil && identity.SystemUUID != "" {
			site, _ = h.Mirrors.LastSiteForMachine(r.Context(), identity.SystemUUID)
		}
		m, err := h.BuildForSite(r.Context(), id, baseURL(r), identity, site)
		if err != nil {
			writeModelErr(w, err)
			return
		}
		respondJSON(w, http.StatusOK, m)
	}
}

// Build is the no-site path retained for existing tests. Internally it
// delegates to BuildForSite with an empty site.
func (h *ManifestHandler) Build(ctx context.Context, id model.ID, base string, identity match.Identity) (Manifest, error) {
	return h.BuildForSite(ctx, id, base, identity, "")
}

// BuildForSite constructs the manifest, picking a mirror for the
// machine's site if Mirrors is configured. Payload URLs (iso-wim,
// driver, software, unattend) point at the mirror's BaseURL when one
// matches; otherwise they point at base (the primary).
func (h *ManifestHandler) BuildForSite(ctx context.Context, id model.ID, base string, identity match.Identity, site string) (Manifest, error) {
	var (
		res resolve.Resolved
		err error
	)
	if (identity == match.Identity{}) {
		res, err = h.Resolver.Resolve(ctx, id)
	} else {
		res, err = h.Resolver.ResolveForMachine(ctx, id, identity)
	}
	if err != nil {
		return Manifest{}, err
	}
	// Pick the payload base URL: a site-matched mirror if available,
	// otherwise the primary's base. Unattend always stays on the
	// primary (it is computed server-side and small).
	payloadBase := base
	if h.Mirrors != nil {
		mirror, ok, err := h.Mirrors.PickFor(ctx, site)
		if err == nil && ok {
			payloadBase = mirror.BaseURL
		}
	}
	m := Manifest{ImageID: id, BaseURL: base, Warnings: res.Diagnostics}
	if site != "" {
		m.Warnings = append(m.Warnings, "payload site: "+site)
	}
	// Ensure the machine record exists and carries a server-minted agent_id,
	// then hand it to the client for deploy-time registry provisioning.
	// Best-effort: a failure must not break the deploy.
	if h.Inventory != nil && identity.SystemUUID != "" {
		if rec, uerr := h.Inventory.UpsertFromIdentity(ctx, identity); uerr == nil {
			m.AgentID = rec.AgentID
			// Bind the machine to the image being deployed so the agent's
			// /self can resolve this image's software set post-OS. A
			// menu-driven deploy otherwise leaves the machine unbound and
			// the agent finds nothing to install. Preserve any
			// operator-set binding fields (name/OU/groups); only set the
			// image.
			binding, berr := h.Inventory.GetBinding(ctx, rec.ID)
			if berr != nil {
				binding = model.MachineBinding{MachineID: rec.ID}
			}
			imgID := id
			binding.ImageID = &imgID
			if err := h.Inventory.UpsertBinding(ctx, binding); err != nil {
				m.Warnings = append(m.Warnings, "image binding: "+err.Error())
			}
		} else {
			m.Warnings = append(m.Warnings, "agent provisioning: "+uerr.Error())
		}
	}
	if res.ISO != nil && res.ISO.DeployReady() {
		// Boot-the-media deploy: hand the client the whole extracted media
		// tree. URL is the media index (every file + size); Base is the
		// prefix to join with each index path. The Boot Client mirrors the
		// tree onto a FAT32 boot partition and lets Setup run. Gated on
		// DeployReady so an un-prepared or non-bootable ISO is never
		// offered.
		base := fmt.Sprintf("%s/payload/iso/%d/", payloadBase, int64(res.ISO.ID))
		m.Items = append(m.Items, ManifestItem{
			Role: "iso-media",
			URL:  base + "index.json",
			Base: base,
			Size: res.ISO.SizeBytes,
			OS:   res.ISO.OSType,
			Name: res.ISO.Name,
		})
	} else if res.ISO != nil {
		// Explain WHY no media is offered, so the Boot Client logs a
		// usable reason instead of a bare "no iso-media".
		switch {
		case !res.ISO.Extracted():
			m.Warnings = append(m.Warnings, fmt.Sprintf("iso %q not extracted; upload or Extract it", res.ISO.Name))
		case res.ISO.PrepError != "":
			m.Warnings = append(m.Warnings, fmt.Sprintf("iso %q boot-media prep failed (%s); Re-prepare it", res.ISO.Name, res.ISO.PrepError))
		case !res.ISO.BootloaderPresent:
			m.Warnings = append(m.Warnings, fmt.Sprintf("iso %q has no UEFI bootloader recorded; Re-prepare it (it may predate boot-media prep)", res.ISO.Name))
		default:
			m.Warnings = append(m.Warnings, fmt.Sprintf("iso %q is not deploy-ready", res.ISO.Name))
		}
	}
	// Driver packages matched against reported hardware (Phase 4). The
	// Boot Client injects each one into the applied image.
	for _, d := range res.Drivers {
		if d.StoragePath == "" {
			// Skip packages that have a row but no uploaded payload yet.
			continue
		}
		m.Items = append(m.Items, ManifestItem{
			Role: "driver",
			URL:  fmt.Sprintf("%s/payload/drivers/%d", payloadBase, int64(d.ID)),
			Size: d.SizeBytes,
			Name: d.Name,
		})
	}
	// Software items. Loadout resolution (Phase 7) extends res.Software;
	// the manifest turns the resolved list into URLs -- but only for
	// packages that actually have a servable single-blob payload. A
	// package with no payload (never uploaded, or a multi-file package
	// whose files the agent fetches by name post-OS) must NOT appear as a
	// boot-time download: the Boot Client would 404 and abort the deploy.
	// This mirrors the driver handling above (skip + warn).
	for _, link := range res.Software {
		if h.Software != nil {
			pkg, err := h.Software.Get(ctx, link.PackageID)
			if err != nil {
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("software package %d referenced but missing; skipped", int64(link.PackageID)))
				continue
			}
			if pkg.StoragePath == "" {
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("software package %q (%d) has no uploaded payload; skipped",
						pkg.Name, int64(link.PackageID)))
				continue
			}
		}
		m.Items = append(m.Items, ManifestItem{
			Role: "software",
			URL:  fmt.Sprintf("%s/payload/software/%d", payloadBase, int64(link.PackageID)),
		})
	}
	if res.Unattend != nil {
		// Thread the requesting machine's SMBIOS UUID through to the
		// unattend endpoint so it can layer the binding's MachineName /
		// TargetOU onto the generated XML — exactly what makes re-
		// imaging preserve identity (§4.3) and what makes the joined
		// AD name match the prepared object.
		unattendURL := fmt.Sprintf("%s/payload/unattend/%d", base, int64(id))
		if identity.SystemUUID != "" {
			unattendURL += "?uuid=" + url.QueryEscape(identity.SystemUUID)
		}
		m.Items = append(m.Items, ManifestItem{
			Role: "unattend",
			URL:  unattendURL,
			Name: res.Unattend.Name,
		})
		// Phase 10: if the unattend has a domain-join section and the
		// machine is in inventory with a binding, prepare the AD object
		// before the OS is laid down so JoinDomain succeeds on first
		// boot.
		if h.AD != nil && h.Inventory != nil && identity.SystemUUID != "" {
			if err := h.prepareAD(ctx, res.Unattend, identity, &m); err != nil {
				m.Warnings = append(m.Warnings, "ad: "+err.Error())
			}
		}
	}
	return m, nil
}

// prepareAD inspects the resolved unattend for a domain_join section and,
// if one is configured and the machine has a binding, calls the Domain
// Integration Service to delete-and-replace the computer object. Also
// fires for agent-driven join (image_domain_join.enabled) even when the
// unattend itself has no legacy DomainJoin block — the old AD object must
// still be cleaned up before the agent re-joins.
func (h *ManifestHandler) prepareAD(ctx context.Context, ua *model.Unattend, identity match.Identity, m *Manifest) error {
	settings, err := unattend.Parse(ua.SettingsJSON)
	hasDomainJoin := err == nil && settings.DomainJoin != nil

	// Also check the agent-driven join config for this image, and capture
	// its OU so we can fall back to it below. Agent-driven join (the common
	// case) configures the OU once on the image rather than per-machine.
	var imageJoinOU string
	if h.DomainJoin != nil {
		if cfg, derr := h.DomainJoin.Get(ctx, m.ImageID); derr == nil && cfg.Enabled && cfg.Domain != "" {
			hasDomainJoin = true
			imageJoinOU = cfg.OU
		}
	}
	if !hasDomainJoin {
		return nil
	}
	machine, err := h.Inventory.GetByUUID(ctx, identity.SystemUUID)
	if err != nil {
		return nil // no binding yet — nothing to prepare
	}
	binding, err := h.Inventory.GetBinding(ctx, machine.ID)
	if err != nil || binding.MachineName == "" {
		return nil
	}
	// Effective OU: the per-machine binding overrides the image default,
	// mirroring the domain-join credential handler. WITHOUT the image-level
	// fallback, an agent-driven image (OU set on the image, no per-machine
	// TargetOU) skipped AD cleanup entirely — so on a re-image the stale
	// computer object survived and the agent's re-join failed with "access
	// denied".
	ou := binding.TargetOU
	if ou == "" {
		ou = imageJoinOU
	}
	if ou == "" {
		return nil
	}
	dn, err := h.AD.PrepareComputer(ctx, binding.MachineName, ou, binding.GroupMemberships)
	if err != nil {
		return err
	}
	if dn != "" {
		m.Warnings = append(m.Warnings, "ad: prepared computer object at "+dn)
	}
	return nil
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

package payload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
)

// Manifest is the Boot Client's instruction sheet: a flat list of URLs to
// fetch, with each entry's role and intended on-disk path. The Boot Client
// is a fact reporter and a step executor — it does not compute any of this.
type Manifest struct {
	ImageID  model.ID      `json:"image_id"`
	BaseURL  string        `json:"base_url"`
	Items    []ManifestItem `json:"items"`
	Warnings []string      `json:"warnings,omitempty"`
}

// ManifestItem is one downloadable payload.
type ManifestItem struct {
	Role  string `json:"role"`           // "iso-wim", "driver", "software", "unattend"
	URL   string `json:"url"`            // absolute or root-relative
	Size  int64  `json:"size_bytes,omitempty"`
	OS    string `json:"os_type,omitempty"`
	Name  string `json:"name,omitempty"`
}

// ManifestHandler returns the resolver-backed manifest for a given image,
// fronted by HTTP. It is the endpoint the Boot Client calls after the
// operator picks a configuration.
type ManifestHandler struct {
	Resolver *resolve.Resolver
}

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
		m, err := h.Build(r.Context(), id, baseURL(r), identity)
		if err != nil {
			writeModelErr(w, err)
			return
		}
		respondJSON(w, http.StatusOK, m)
	}
}

// Build constructs the manifest in code (test-friendly entry point). When
// identity is zero-valued the resolver runs without driver matching, which
// is what the portal's "view manifest" link does.
func (h *ManifestHandler) Build(ctx context.Context, id model.ID, base string, identity match.Identity) (Manifest, error) {
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
	m := Manifest{ImageID: id, BaseURL: base, Warnings: res.Diagnostics}
	if res.ISO != nil {
		// If extraction has happened, StoragePath points at the WIM/ESD
		// inside the extracted tree (iso/{id}/files/...). Serve it from
		// /payload/iso/{id}/{path-inside-files}.
		if isExtracted := strings.Contains(res.ISO.StoragePath, "/files/"); isExtracted {
			afterFiles := res.ISO.StoragePath
			if i := strings.Index(afterFiles, "/files/"); i >= 0 {
				afterFiles = afterFiles[i+len("/files/"):]
			}
			m.Items = append(m.Items, ManifestItem{
				Role: "iso-wim",
				URL:  fmt.Sprintf("%s/payload/iso/%d/%s", base, int64(res.ISO.ID), afterFiles),
				Size: res.ISO.SizeBytes,
				OS:   res.ISO.OSType,
				Name: res.ISO.Name,
			})
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
			URL:  fmt.Sprintf("%s/payload/drivers/%d", base, int64(d.ID)),
			Size: d.SizeBytes,
			Name: d.Name,
		})
	}
	// Software items. Loadout resolution (Phase 7) extends res.Software;
	// the manifest just turns the resolved list into URLs.
	for _, link := range res.Software {
		m.Items = append(m.Items, ManifestItem{
			Role: "software",
			URL:  fmt.Sprintf("%s/payload/software/%d", base, int64(link.PackageID)),
		})
	}
	if res.Unattend != nil {
		// Unattend generation lands in Phase 5; expose the endpoint shape
		// now so the Boot Client manifest format is stable.
		m.Items = append(m.Items, ManifestItem{
			Role: "unattend",
			URL:  fmt.Sprintf("%s/payload/unattend/%d", base, int64(id)),
			Name: res.Unattend.Name,
		})
	}
	return m, nil
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

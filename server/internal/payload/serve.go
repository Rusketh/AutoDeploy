package payload

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/unattend"
)

// Service serves payload uploads and downloads. Routes are registered on a
// caller-supplied mux. Authentication and access-PIN gating land in Phase 11;
// the routes are open in Phase 2 so the Boot Client and operator tooling
// can be developed against them.
type Service struct {
	Blobs    *storage.BlobStore
	ISOs     *model.ISORepo
	Drivers  *model.DriverPackageRepo
	Software *model.SoftwarePackageRepo
	// Resolver lets the unattend endpoint pick the nearest-wins unattend
	// for a given image. Optional; nil disables the endpoint.
	Resolver *resolve.Resolver
	// Inventory lets the unattend endpoint look up the requesting
	// machine's binding by SMBIOS UUID and inject per-machine identity
	// (computer name, target OU) into the generated XML. Optional; nil
	// disables per-machine identity injection and the unattend falls
	// back to the shared template's values.
	Inventory *model.InventoryRepo
	// DomainJoin, when non-nil, lets the unattend endpoint suppress the
	// legacy <UnattendedJoin> block for images configured to join AD via
	// the agent (so Setup doesn't also attempt -- and hang on -- an online
	// join during specialize).
	DomainJoin *model.DomainJoinRepo
	// Throttle, when non-nil, bounds concurrent /payload/* requests so a
	// 500-machine PXE burst queues rather than thrashes file descriptors.
	Throttle *Throttle
	// OnBytesServed is called with the byte count of each completed
	// payload response so the operator can wire it into metrics.
	OnBytesServed func(int64)
	// RequireAuth, when non-nil, is called at the top of every
	// operator-facing upload/extract handler. It should return true if
	// the request is authenticated. When it returns false, it must
	// write an HTTP error response itself (typically 401).
	RequireAuth func(http.ResponseWriter, *http.Request) bool
}

// Register mounts payload routes on mux:
//
//	PUT  /api/v1/isos/{id}/upload      — upload an ISO file (multipart or raw)
//	POST /api/v1/isos/{id}/extract     — extract the uploaded ISO contents
//	PUT  /api/v1/drivers/{id}/upload   — upload a driver-package blob
//	PUT  /api/v1/software/{id}/upload  — upload a software-installer blob
//	GET  /payload/iso/{id}/{path...}   — serve an extracted ISO file
//	GET  /payload/drivers/{id}         — serve a driver-package blob
//	GET  /payload/software/{id}        — serve a software-installer blob
//
// All downloads support HTTP Range so a Boot Client can resume an
// interrupted fetch over a flaky link.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/isos/{id}/upload", s.uploadISO)
	mux.HandleFunc("POST /api/v1/isos/{id}/extract", s.extractISO)
	mux.HandleFunc("PUT /api/v1/drivers/{id}/upload", s.uploadDriver)
	mux.HandleFunc("POST /api/v1/drivers/{id}/extract", s.extractDriver)
	mux.HandleFunc("PUT /api/v1/software/{id}/upload", s.uploadSoftware)

	// Throttle the download routes; uploads and the lightweight
	// unattend generator are not in the burst path.
	// Media index: lists every file in the extracted ISO tree so the
	// Boot Client can mirror the whole media (boot-the-media deploy),
	// not just pull a single WIM. Registered BEFORE the {id}/ content
	// route is fine: ServeMux longest-pattern matching sends
	// /payload/iso/{id}/index.json here and everything else to content.
	mux.HandleFunc("GET /payload/iso/{id}/index.json", s.serveISOIndex)
	mux.Handle("GET /payload/iso/{id}/", s.throttleHandler(http.HandlerFunc(s.serveISOContent)))
	mux.Handle("GET /payload/drivers/{id}", s.throttleHandler(http.HandlerFunc(s.serveDriver)))
	mux.Handle("GET /payload/software/{id}", s.throttleHandler(http.HandlerFunc(s.serveSoftware)))
	// Multi-file: agent downloads each file by name from the
	// per-package files directory the portal upload handler writes
	// into. {name} is sanitized server-side; BlobStore.Resolve is
	// the backstop against path traversal.
	mux.Handle("GET /payload/software/{id}/files/{name}", s.throttleHandler(http.HandlerFunc(s.serveSoftwareFile)))
	mux.Handle("GET /payload/software/{id}/bundle/{name}", s.throttleHandler(http.HandlerFunc(s.serveSoftwareBundle)))
	mux.HandleFunc("GET /payload/unattend/{id}", s.serveUnattend)
}

func (s *Service) throttleHandler(h http.Handler) http.Handler {
	if s.Throttle == nil {
		return h
	}
	return s.Throttle.Wrap(h)
}

// serveUnattend resolves the image, picks the nearest-wins unattend, and
// returns the generated unattend.xml. The path id is the IMAGE id (not the
// unattend id), because the choice of unattend depends on the image's
// inheritance chain — only the resolver knows which one applies.
//
// If a ?uuid=<smbios-uuid> query parameter is present and a binding
// exists for that machine, per-machine identity (computer name, target
// OU) is layered onto the resolved unattend so the deployed machine
// matches the AD object the manifest endpoint prepared. Without the
// query param the endpoint behaves as before — useful for the portal's
// "preview XML" link.
//
// Logging note: the generated XML contains the local-admin password and
// any domain-join password. The endpoint logs only the fact of access
// (which it gets for free from the request logger) — never the bytes.
func (s *Service) serveUnattend(w http.ResponseWriter, r *http.Request) {
	if s.Resolver == nil {
		http.Error(w, "unattend generation not configured", http.StatusServiceUnavailable)
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.Resolver.Resolve(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	if res.Unattend == nil {
		http.Error(w, "no unattend resolved for this image", http.StatusNotFound)
		return
	}
	settings, err := unattend.Parse(res.Unattend.SettingsJSON)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// If the resolved image joins AD via the agent, drop the legacy unattend
	// UnattendedJoin block: the agent performs the join after first boot, and
	// leaving the block in makes Setup also attempt an online join during
	// specialize (which hangs ~15m and fails when the DC isn't reachable yet).
	if s.DomainJoin != nil && settings.DomainJoin != nil {
		if cfg, derr := s.DomainJoin.Get(r.Context(), res.ImageID); derr == nil && cfg.Enabled {
			settings.DomainJoin = nil
		}
	}
	// Per-machine identity injection. The boot client adds ?uuid=... to
	// the URL the manifest gave it; we resolve that to the bound name +
	// OU and override the unattend's computer-naming and domain-join
	// settings so re-imaging preserves identity (§4.3) and the bound AD
	// object matches the joined name.
	if uuid := strings.TrimSpace(r.URL.Query().Get("uuid")); uuid != "" && s.Inventory != nil {
		applyBindingIdentity(r, s.Inventory, uuid, &settings)
	}
	xml, err := unattend.Generate(settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="unattend.xml"`)
	_, _ = w.Write(xml)
}

// applyBindingIdentity looks up the binding for the given SMBIOS UUID
// and, if present, layers its MachineName and TargetOU onto the
// settings before XML generation. Lookup failures are silent: the
// generator still produces a valid XML, just without per-machine
// identity. The decision to layer rather than replace means the
// operator's catalog choices (locale, software, RDP/policies, …) are
// kept; only the identity-bearing fields are overridden.
func applyBindingIdentity(r *http.Request, inv *model.InventoryRepo, uuid string, settings *unattend.Settings) {
	machine, err := inv.GetByUUID(r.Context(), uuid)
	if err != nil {
		return
	}
	binding, err := inv.GetBinding(r.Context(), machine.ID)
	if err != nil {
		return
	}
	// Per-machine identity for name-template placeholder expansion
	// (%serial%, %uuid%, %agent%). The generator expands NameTemplate
	// using this.
	settings.NameIdentity = unattend.NameIdentity{
		Serial: machine.SystemSerial,
		UUID:   machine.SystemUUID,
		Agent:  machine.AgentID,
	}
	// An explicit per-machine name from the binding wins over the template
	// (e.g. an operator pinned "LAB-01"); set it as a literal template.
	if binding.MachineName != "" {
		settings.NameTemplate = binding.MachineName
	}
	if binding.TargetOU != "" && settings.DomainJoin != nil {
		settings.DomainJoin.OU = binding.TargetOU
	}
}

// checkAuth returns false (and writes a 401) if RequireAuth is
// configured and the request fails the check. Handlers call this at
// the top and return early on false.
func (s *Service) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.RequireAuth != nil && !s.RequireAuth(w, r) {
		return false
	}
	return true
}

// uploadISO accepts a raw octet-stream PUT body and writes it to
// data/iso/{id}/source.iso, updating the ISO row with the storage path.
func (s *Service) uploadISO(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	iso, err := s.ISOs.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	rel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "source.iso"))
	n, err := s.Blobs.WriteStream(rel, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	iso.StoragePath = rel
	iso.SizeBytes = n
	if err := s.ISOs.Update(r.Context(), iso); err != nil {
		writeModelErr(w, err)
		return
	}
	// Auto-extract so the ISO is immediately deployable -- the WIM/ESD
	// inside is what the Boot Client applies, and the manifest only emits
	// an iso-wim item once storage_path points inside the extracted tree.
	// A failure here is non-fatal: the source blob is safely stored and
	// the operator can re-run POST /api/v1/isos/{id}/extract.
	prep, exErr := ExtractAndRecord(r.Context(), s.Blobs, s.ISOs, id)
	resp := map[string]any{
		"id":           iso.ID,
		"storage_path": rel,
		"size_bytes":   n,
		"extracted":    exErr == nil && prep.InstallRel != "",
		"install_path": prep.InstallRel,
		"swm_parts":    prep.SWMParts,
		"format":       prep.Format,
	}
	if exErr != nil {
		resp["extract_error"] = exErr.Error()
	}
	if prep.Err != "" {
		resp["prep_error"] = prep.Err
	}
	respondJSON(w, http.StatusOK, resp)
}

// extractISO (re-)unpacks the uploaded ISO into data/iso/{id}/files/ and
// prepares it as FAT32 boot media (splitting an oversized install.wim into
// .swm parts). It delegates to ExtractAndRecord so the manual Extract and
// the upload auto-extract share one code path.
func (s *Service) extractISO(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prep, err := ExtractAndRecord(r.Context(), s.Blobs, s.ISOs, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("extract: %v", err), http.StatusInternalServerError)
		return
	}
	iso, err := s.ISOs.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":           iso.ID,
		"install_path": prep.InstallRel,
		"format":       prep.Format,
		"swm_parts":    prep.SWMParts,
		"bootloader":   prep.BootloaderPresent,
		"prep_error":   prep.Err,
		"storage_path": iso.StoragePath,
	})
}

func (s *Service) uploadDriver(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pkg, err := s.Drivers.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	rel := filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(id)), "payload.bin"))
	n, err := s.Blobs.WriteStream(rel, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pkg.StoragePath = rel
	pkg.SizeBytes = n
	if err := s.Drivers.Update(r.Context(), pkg); err != nil {
		writeModelErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":           pkg.ID,
		"storage_path": pkg.StoragePath,
		"size_bytes":   pkg.SizeBytes,
	})
}

func (s *Service) uploadSoftware(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pkg, err := s.Software.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	rel := filepath.ToSlash(filepath.Join("software", fmt.Sprint(int64(id)), "payload.bin"))
	n, err := s.Blobs.WriteStream(rel, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pkg.StoragePath = rel
	pkg.SizeBytes = n
	if err := s.Software.Update(r.Context(), pkg); err != nil {
		writeModelErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":           pkg.ID,
		"storage_path": pkg.StoragePath,
		"size_bytes":   pkg.SizeBytes,
	})
}

// MediaIndex is the JSON the Boot Client fetches to learn every file in
// an extracted ISO's media tree. Files[].Path is relative to the media
// root (e.g. "sources/install.wim"); the client fetches each from
// GET /payload/iso/{id}/{path}.
type MediaIndex struct {
	ISOID int64            `json:"iso_id"`
	Files []MediaIndexFile `json:"files"`
}

// MediaIndexFile is one file in the media tree.
type MediaIndexFile struct {
	Path string `json:"path"`
	Size int64  `json:"size_bytes"`
}

// serveISOIndex returns the recursive file list of the extracted ISO so
// the Boot Client can mirror the full media. 404 if the ISO has not been
// extracted yet (no files/ tree). This is what makes a boot-the-media
// deploy possible without the client guessing filenames.
func (s *Service) serveISOIndex(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files"))
	entries, err := s.Blobs.ListTree(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(entries) == 0 {
		http.Error(w, "iso not extracted; POST /api/v1/isos/{id}/extract first", http.StatusNotFound)
		return
	}
	idx := MediaIndex{ISOID: int64(id)}
	for _, e := range entries {
		idx.Files = append(idx.Files, MediaIndexFile{Path: e.Name, Size: e.Size})
	}
	respondJSON(w, http.StatusOK, idx)
}

func (s *Service) serveISOContent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Anything after /payload/iso/{id}/ is the path inside the extracted tree.
	prefix := fmt.Sprintf("/payload/iso/%d/", int64(id))
	sub := strings.TrimPrefix(r.URL.Path, prefix)
	if sub == "" || sub == r.URL.Path {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	rel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files", sub))
	s.serveBlob(w, r, rel)
}

func (s *Service) serveDriver(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pkg, err := s.Drivers.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	if pkg.StoragePath == "" {
		http.Error(w, "driver package not uploaded", http.StatusNotFound)
		return
	}
	s.serveBlob(w, r, pkg.StoragePath)
}

func (s *Service) serveSoftware(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pkg, err := s.Software.Get(r.Context(), id)
	if err != nil {
		writeModelErr(w, err)
		return
	}
	if pkg.StoragePath == "" {
		http.Error(w, "software package not uploaded", http.StatusNotFound)
		return
	}
	s.serveBlob(w, r, pkg.StoragePath)
}

// serveSoftwareFile streams one named file from a multi-file
// package. The filename is sanitised (no path separators, no ..) and
// BlobStore.Resolve is the final containment check; together they
// prevent a request from reaching anything outside the package's
// files directory.
func (s *Service) serveSoftwareFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.Error(w, "bad filename", http.StatusBadRequest)
		return
	}
	if _, err := s.Software.Get(r.Context(), id); err != nil {
		writeModelErr(w, err)
		return
	}
	rel := "software/" + fmt.Sprint(int64(id)) + "/files/" + name
	s.serveBlob(w, r, rel)
}

// serveSoftwareBundle streams one bundle zip (software/<id>/bundle/<name>),
// which the agent extracts into its package workdir. Same sanitisation +
// containment as serveSoftwareFile.
func (s *Service) serveSoftwareBundle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.Error(w, "bad filename", http.StatusBadRequest)
		return
	}
	if _, err := s.Software.Get(r.Context(), id); err != nil {
		writeModelErr(w, err)
		return
	}
	rel := "software/" + fmt.Sprint(int64(id)) + "/bundle/" + name
	s.serveBlob(w, r, rel)
}

// serveBlob streams the file at relative to the response with range
// support via http.ServeContent. Cache headers allow intermediate
// HTTP caches (squid, varnish, a reverse proxy) and the Boot Client
// itself to short-circuit repeat fetches.
//
// Cache-Control is conservative (5 minutes) because operators can swap
// the underlying blob by re-uploading; intermediate caches that respect
// max-age will not serve a stale version for long. ETag is derived from
// mtime+size by http.ServeContent.
func (s *Service) serveBlob(w http.ResponseWriter, r *http.Request, relative string) {
	f, err := s.Blobs.Open(relative)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Aid intermediate caches (squid/varnish/CDN).
	w.Header().Set("Cache-Control", "public, max-age=300")
	cw := &byteCounter{ResponseWriter: w}
	http.ServeContent(cw, r, filepath.Base(relative), info.ModTime(), f)
	if s.OnBytesServed != nil {
		s.OnBytesServed(cw.n)
	}
}

type byteCounter struct {
	http.ResponseWriter
	n int64
}

func (b *byteCounter) Write(p []byte) (int, error) {
	n, err := b.ResponseWriter.Write(p)
	b.n += int64(n)
	return n, err
}

// Flush implements http.Flusher so streaming responses work through the
// byte-counting wrapper.
func (b *byteCounter) Flush() {
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func pathID(r *http.Request) (model.ID, error) {
	raw := r.PathValue("id")
	var id int64
	_, err := fmt.Sscanf(raw, "%d", &id)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", raw)
	}
	return model.ID(id), nil
}

func writeModelErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, model.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, model.ErrValidation):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

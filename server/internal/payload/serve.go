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
	"github.com/rusketh/autodeploy/server/internal/storage"
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
}

// Register mounts payload routes on mux:
//
//   PUT  /api/v1/isos/{id}/upload      — upload an ISO file (multipart or raw)
//   POST /api/v1/isos/{id}/extract     — extract the uploaded ISO contents
//   PUT  /api/v1/drivers/{id}/upload   — upload a driver-package blob
//   PUT  /api/v1/software/{id}/upload  — upload a software-installer blob
//   GET  /payload/iso/{id}/{path...}   — serve an extracted ISO file
//   GET  /payload/drivers/{id}         — serve a driver-package blob
//   GET  /payload/software/{id}        — serve a software-installer blob
//
// All downloads support HTTP Range so a Boot Client can resume an
// interrupted fetch over a flaky link.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/isos/{id}/upload", s.uploadISO)
	mux.HandleFunc("POST /api/v1/isos/{id}/extract", s.extractISO)
	mux.HandleFunc("PUT /api/v1/drivers/{id}/upload", s.uploadDriver)
	mux.HandleFunc("PUT /api/v1/software/{id}/upload", s.uploadSoftware)

	mux.HandleFunc("GET /payload/iso/{id}/", s.serveISOContent)
	mux.HandleFunc("GET /payload/drivers/{id}", s.serveDriver)
	mux.HandleFunc("GET /payload/software/{id}", s.serveSoftware)
}

// uploadISO accepts a raw octet-stream PUT body and writes it to
// data/iso/{id}/source.iso, updating the ISO row with the storage path.
func (s *Service) uploadISO(w http.ResponseWriter, r *http.Request) {
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
	respondJSON(w, http.StatusOK, map[string]any{
		"id":           iso.ID,
		"storage_path": iso.StoragePath,
		"size_bytes":   iso.SizeBytes,
	})
}

// extractISO unpacks the previously uploaded ISO into data/iso/{id}/files/...
// On success it sets the ISO row's storage_path to the WIM/ESD path so the
// resolver can hand it back as a payload URL.
func (s *Service) extractISO(w http.ResponseWriter, r *http.Request) {
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
	srcRel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "source.iso"))
	srcAbs, err := s.Blobs.Resolve(srcRel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := os.Stat(srcAbs); errors.Is(err, os.ErrNotExist) {
		http.Error(w, "iso not uploaded; PUT /api/v1/isos/{id}/upload first", http.StatusBadRequest)
		return
	}
	destAbs, err := s.Blobs.EnsureDir(filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, wimRel, err := ExtractISO(srcAbs, destAbs)
	if err != nil {
		http.Error(w, fmt.Sprintf("extract: %v", err), http.StatusInternalServerError)
		return
	}
	if wimRel != "" {
		iso.StoragePath = filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files", wimRel))
	}
	if err := s.ISOs.Update(r.Context(), iso); err != nil {
		writeModelErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":           iso.ID,
		"bytes":        total,
		"wim_path":     wimRel,
		"storage_path": iso.StoragePath,
	})
}

func (s *Service) uploadDriver(w http.ResponseWriter, r *http.Request) {
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

// serveBlob streams the file at relative to the response with range support
// via http.ServeContent.
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
	http.ServeContent(w, r, filepath.Base(relative), info.ModTime(), f)
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

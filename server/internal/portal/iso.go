package portal

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
)

func registerISORoutes(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/isos", isoList(r))
	get("/portal/isos/new", isoForm(r, model.ISO{}))
	post("/portal/isos", isoCreate(r))
	get("/portal/isos/{id}/edit", isoEdit(r))
	post("/portal/isos/{id}", isoUpdate(r))
	post("/portal/isos/{id}/delete", isoDelete(r))
	post("/portal/isos/{id}/upload", isoUpload(r))
	post("/portal/isos/{id}/extract", isoExtract(r))
}

func isoList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.ISOs.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Per-row reference counts so the operator sees what's in use.
		refs := map[model.ID]int{}
		for _, iso := range v {
			n, _ := r.ISOs.RefCount(req.Context(), iso.ID)
			refs[iso.ID] = n
		}
		render(w, req, r, "iso_list.html", "ISOs", map[string]any{
			"ISOs": v, "Refs": refs,
		})
	}
}

func isoForm(r Repos, iso model.ISO) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "iso_form.html", "New ISO", map[string]any{
			"ISO": iso, "IsNew": true,
		})
	}
}

func isoEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		iso, err := r.ISOs.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		render(w, req, r, "iso_form.html", "Edit ISO: "+iso.Name, map[string]any{
			"ISO": iso, "IsNew": false,
		})
	}
}

func isoCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		out, err := r.ISOs.Create(req.Context(), model.ISO{
			Name:        req.FormValue("name"),
			Description: req.FormValue("description"),
			OSType:      req.FormValue("os_type"),
		})
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/isos/new", http.StatusFound)
			return
		}
		flash(w, "ok", "ISO created — upload the file next.")
		http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", out.ID), http.StatusFound)
	}
}

func isoUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		existing, err := r.ISOs.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		existing.Name = req.FormValue("name")
		existing.Description = req.FormValue("description")
		existing.OSType = req.FormValue("os_type")
		if err := r.ISOs.Update(req.Context(), existing); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
	}
}

func isoDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.ISOs.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/isos", http.StatusFound)
			return
		}
		flash(w, "ok", "ISO deleted.")
		http.Redirect(w, req, "/portal/isos", http.StatusFound)
	}
}

// isoUpload accepts a multipart upload of the ISO file and stores it.
// We do NOT call ParseMultipartForm to avoid buffering the whole ISO in
// memory — instead we read the body as multipart and stream the first
// file part to disk.
func isoUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		iso, err := r.ISOs.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mr, err := req.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart upload", http.StatusBadRequest)
			return
		}
		var n int64
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				flash(w, "err", err.Error())
				http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
				return
			}
			if part.FormName() != "file" {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				continue
			}
			rel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "source.iso"))
			n, err = r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				flash(w, "err", err.Error())
				http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
				return
			}
			iso.StoragePath = rel
			iso.SizeBytes = n
			if err := r.ISOs.Update(req.Context(), iso); err != nil {
				flash(w, "err", err.Error())
				break
			}
			// Auto-extract so the operator doesn't have to remember a
			// second step -- the WIM/ESD inside the ISO is what actually
			// gets deployed. Extraction failure is non-fatal: the upload
			// succeeded, and the manual Extract button remains available.
			wim, exErr := payload.ExtractAndRecord(req.Context(), r.Blobs, r.ISOs, id)
			switch {
			case exErr != nil:
				flash(w, "err", fmt.Sprintf("Uploaded %d bytes, but extraction failed: %v. Use the Extract button to retry.", n, exErr))
			case wim == "":
				flash(w, "err", fmt.Sprintf("Uploaded %d bytes and extracted, but no install.wim/install.esd was found in the ISO. Is this a Windows install ISO?", n))
			default:
				flash(w, "ok", fmt.Sprintf("Uploaded %d bytes and extracted. Ready to deploy (WIM/ESD: %s).", n, wim))
			}
			break
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
	}
}

func isoExtract(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		iso, err := r.ISOs.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		srcRel := filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "source.iso"))
		srcAbs, err := r.Blobs.Resolve(srcRel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		destAbs, err := r.Blobs.EnsureDir(filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, wim, err := payload.ExtractISO(srcAbs, destAbs)
		if err != nil {
			flash(w, "err", "Extract failed: "+err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
			return
		}
		if wim != "" {
			iso.StoragePath = filepath.ToSlash(filepath.Join("iso", fmt.Sprint(int64(id)), "files", wim))
			_ = r.ISOs.Update(req.Context(), iso)
		}
		flash(w, "ok", "Extracted. WIM/ESD: "+wim)
		http.Redirect(w, req, fmt.Sprintf("/portal/isos/%d/edit", id), http.StatusFound)
	}
}

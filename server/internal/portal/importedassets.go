package portal

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerImportRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/import", importList(r))
		post("/portal/import", importUpload(r))
		post("/portal/import/wipe", importWipe(r))
	}
}

// importList renders the imported-assets page: the current staging table plus
// the CSV upload form and the wipe action.
func importList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var assets []model.ImportedAsset
		if r.ImportedAssets != nil {
			assets, _ = r.ImportedAssets.List(req.Context())
		}
		matched := 0
		for _, a := range assets {
			if a.Matched() {
				matched++
			}
		}
		render(w, req, r, "imported_assets.html", "Import assets", map[string]any{
			"Assets":  assets,
			"Total":   len(assets),
			"Matched": matched,
		})
	}
}

// importUpload accepts a CSV file, parses it, and upserts the rows keyed on
// (serial, model). The apply mode (fill-only vs overwrite) is read from the
// form and stamped onto every imported row.
func importUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.ImportedAssets == nil {
			flash(w, "err", "Asset import is not available.")
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			flash(w, "err", "Failed to read upload: "+err.Error())
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		overwrite := req.FormValue("apply_mode") == "overwrite"

		f, hdr, err := req.FormFile("file")
		if err != nil {
			flash(w, "err", "Choose a CSV file to import.")
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		defer f.Close()

		assets, skipped, err := model.ParseCSV(f)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		for i := range assets {
			assets[i].Overwrite = overwrite
		}
		inserted, updated, err := r.ImportedAssets.Upsert(req.Context(), assets)
		if err != nil {
			flash(w, "err", "Import failed: "+err.Error())
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}

		mode := "fill-only"
		if overwrite {
			mode = "overwrite"
		}
		if r.Logs != nil {
			user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
			fields, _ := json.Marshal(map[string]any{
				"file": filename(hdr), "inserted": inserted, "updated": updated,
				"skipped": len(skipped), "apply_mode": mode,
			})
			_ = r.Logs.Append(req.Context(), model.LogEvent{
				Component: "inventory",
				Actor:     user.Username,
				Action:    "asset.import",
				Target:    "imported_asset:" + strconv.Itoa(inserted+updated),
				Fields:    string(fields),
			})
		}

		msg := "Imported " + strconv.Itoa(inserted) + " new and updated " +
			strconv.Itoa(updated) + " asset(s) (" + mode + ")."
		if len(skipped) > 0 {
			msg += " Skipped " + strconv.Itoa(len(skipped)) + " invalid row(s)."
		}
		flash(w, "ok", msg)
		http.Redirect(w, req, "/portal/import", http.StatusFound)
	}
}

// importWipe clears the entire imported-asset staging table.
func importWipe(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.ImportedAssets == nil {
			flash(w, "err", "Asset import is not available.")
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		n, err := r.ImportedAssets.WipeAll(req.Context())
		if err != nil {
			flash(w, "err", "Wipe failed: "+err.Error())
			http.Redirect(w, req, "/portal/import", http.StatusFound)
			return
		}
		if r.Logs != nil {
			user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
			fields, _ := json.Marshal(map[string]any{"removed": n})
			_ = r.Logs.Append(req.Context(), model.LogEvent{
				Component: "inventory",
				Actor:     user.Username,
				Action:    "asset.wipe",
				Target:    "imported_asset",
				Fields:    string(fields),
			})
		}
		flash(w, "ok", "Wiped "+strconv.FormatInt(n, 10)+" imported asset(s).")
		http.Redirect(w, req, "/portal/import", http.StatusFound)
	}
}

// filename returns a multipart header's filename, or "upload.csv" if absent.
func filename(hdr *multipart.FileHeader) string {
	if hdr != nil && hdr.Filename != "" {
		return hdr.Filename
	}
	return "upload.csv"
}

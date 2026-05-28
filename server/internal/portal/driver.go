package portal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerDriverRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/drivers", driverList(r))
		get("/portal/drivers/new", driverForm(r, model.DriverPackage{}, true))
		post("/portal/drivers", driverCreate(r))
		get("/portal/drivers/{id}/edit", driverEdit(r))
		post("/portal/drivers/{id}", driverUpdate(r))
		post("/portal/drivers/{id}/delete", driverDelete(r))
		post("/portal/drivers/{id}/upload", driverUpload(r))
		post("/portal/drivers/{id}/preview", driverPreviewForm(r))
	}
}

func driverList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Drivers.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "driver_list.html", "Driver packages", map[string]any{"Drivers": v})
	}
}

// uiFilter is the form-shaped flat filter view: a list of (key, value).
type uiFilter struct {
	ID    model.ID
	Pairs []uiPair
}
type uiPair struct{ Key, Value string }

func driverForm(r Repos, p model.DriverPackage, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		uf := make([]uiFilter, 0, len(p.Filters))
		for _, f := range p.Filters {
			var m map[string]string
			_ = json.Unmarshal([]byte(f.FilterJSON), &m)
			u := uiFilter{ID: f.ID}
			for k, v := range m {
				u.Pairs = append(u.Pairs, uiPair{Key: k, Value: v})
			}
			uf = append(uf, u)
		}
		title := "New driver package"
		if !isNew {
			title = "Edit driver package: " + p.Name
		}
		render(w, req, r, "driver_form.html", title, map[string]any{
			"Driver":      p,
			"Filters":     uf,
			"AllowedKeys": allowedKeysList(),
			"IsNew":       isNew,
		})
	}
}

func allowedKeysList() []string {
	out := make([]string, 0, len(match.AllowedKeys))
	for k := range match.AllowedKeys {
		out = append(out, k)
	}
	// deterministic order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func driverEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		driverForm(r, p, false)(w, req)
	}
}

// buildDriverFromForm reads the form's repeating filters into a
// DriverPackage. Each filter is a row of [keys[], values[]] pairs;
// rows themselves are delimited by an explicit filter index marker.
func buildDriverFromForm(req *http.Request) (model.DriverPackage, error) {
	if err := req.ParseForm(); err != nil {
		return model.DriverPackage{}, err
	}
	// We use bracketed names like filter_keys_0[], filter_vals_0[] so each
	// filter's set of constraints stays grouped. The template emits a
	// hidden filter_index[] list naming each filter's suffix.
	pkg := model.DriverPackage{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
	}
	for _, idx := range req.Form["filter_index[]"] {
		keys := req.Form["filter_keys_"+idx+"[]"]
		vals := req.Form["filter_vals_"+idx+"[]"]
		m := map[string]string{}
		for i := 0; i < len(keys) && i < len(vals); i++ {
			k := strings.TrimSpace(keys[i])
			v := strings.TrimSpace(vals[i])
			if k == "" {
				continue
			}
			m[k] = v
		}
		if len(m) == 0 {
			continue
		}
		raw, _ := json.Marshal(m)
		pkg.Filters = append(pkg.Filters, model.DriverFilter{FilterJSON: string(raw)})
	}
	return pkg, nil
}

func driverCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p, err := buildDriverFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/drivers/new", http.StatusFound)
			return
		}
		out, err := r.Drivers.Create(req.Context(), p)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/drivers/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Driver package created — upload the payload next.")
		http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", out.ID), http.StatusFound)
	}
}

func driverUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := buildDriverFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", id), http.StatusFound)
			return
		}
		existing, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		p.ID = id
		p.StoragePath = existing.StoragePath
		p.SizeBytes = existing.SizeBytes
		if err := r.Drivers.Update(req.Context(), p); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", id), http.StatusFound)
	}
}

func driverDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Drivers.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/drivers", http.StatusFound)
	}
}

func driverUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		pkg, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mr, err := req.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart upload", http.StatusBadRequest)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				flash(w, "err", err.Error())
				break
			}
			if part.FormName() != "file" {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				continue
			}
			rel := filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(id)), "payload.bin"))
			n, err := r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				flash(w, "err", err.Error())
				break
			}
			pkg.StoragePath = rel
			pkg.SizeBytes = n
			if err := r.Drivers.Update(req.Context(), pkg); err != nil {
				flash(w, "err", err.Error())
			} else {
				flash(w, "ok", fmt.Sprintf("Uploaded %d bytes.", n))
			}
			break
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", id), http.StatusFound)
	}
}

// driverPreviewForm: evaluate the driver's filters against an identity
// the operator types in. Re-renders the edit page with the result.
func driverPreviewForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		pkg, err := r.Drivers.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id2 := match.Identity{
			SystemManufacturer: req.FormValue("p_system_manufacturer"),
			SystemProduct:      req.FormValue("p_system_product"),
			SystemSerial:       req.FormValue("p_system_serial"),
			SystemUUID:         req.FormValue("p_system_uuid"),
			BIOSVendor:         req.FormValue("p_bios_vendor"),
			BIOSVersion:        req.FormValue("p_bios_version"),
			BoardManufacturer:  req.FormValue("p_board_manufacturer"),
			BoardProduct:       req.FormValue("p_board_product"),
			BoardSerial:        req.FormValue("p_board_serial"),
		}
		type fr struct {
			JSON    string
			Matches bool
			Err     string
		}
		var results []fr
		matches := false
		for _, f := range pkg.Filters {
			one := fr{JSON: f.FilterJSON}
			parsed, perr := match.ParseFilter(f.FilterJSON)
			if perr != nil {
				one.Err = perr.Error()
			} else if parsed.Matches(id2) {
				one.Matches = true
				matches = true
			}
			results = append(results, one)
		}

		uf := make([]uiFilter, 0, len(pkg.Filters))
		for _, f := range pkg.Filters {
			var m map[string]string
			_ = json.Unmarshal([]byte(f.FilterJSON), &m)
			u := uiFilter{ID: f.ID}
			for k, v := range m {
				u.Pairs = append(u.Pairs, uiPair{Key: k, Value: v})
			}
			uf = append(uf, u)
		}
		render(w, req, r, "driver_form.html", "Edit driver package: "+pkg.Name, map[string]any{
			"Driver":      pkg,
			"Filters":     uf,
			"AllowedKeys": allowedKeysList(),
			"IsNew":       false,
			"Preview": map[string]any{
				"Identity":       id2,
				"Results":        results,
				"PackageMatches": matches,
			},
		})
	}
}

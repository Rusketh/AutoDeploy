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
	"github.com/rusketh/autodeploy/server/internal/payload"
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
		post("/portal/drivers/{id}/extract", driverExtract(r))
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

// machineFilterChoice is one entry in the "use machine as filter"
// dropdown on the driver form. The label is what the operator sees;
// the manufacturer/product values are what get written into the new
// filter constraint when the operator picks one.
type machineFilterChoice struct {
	ID           int64
	Label        string
	Manufacturer string
	Product      string
}

func driverForm(r Repos, p model.DriverPackage, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		driverFormRender(w, req, r, p, isNew, nil, "")
	}
}

// driverFormRender is the shared rendering path for the driver edit
// page. preview is the optional filter-evaluation result; extractMsg
// is an optional flash-style line shown next to the metadata table
// (set by the extract handler).
func driverFormRender(w http.ResponseWriter, req *http.Request, r Repos, p model.DriverPackage, isNew bool, preview map[string]any, extractMsg string) {
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
	// Build the "use machine as filter" dropdown. Only machines whose
	// SMBIOS makes a meaningful filter (manufacturer + product) are
	// included; an empty list collapses the helper UI gracefully.
	var machines []machineFilterChoice
	if !isNew && r.Inventory != nil {
		all, _ := r.Inventory.List(req.Context())
		for _, m := range all {
			if strings.TrimSpace(m.SystemManufacturer) == "" || strings.TrimSpace(m.SystemProduct) == "" {
				continue
			}
			machines = append(machines, machineFilterChoice{
				ID:           int64(m.ID),
				Label:        m.SystemManufacturer + " " + m.SystemProduct,
				Manufacturer: m.SystemManufacturer,
				Product:      m.SystemProduct,
			})
		}
	}
	// Surface previously-extracted INF metadata when the operator
	// returns to the edit page after Save / cancel / browser back.
	var metadata *payload.DriverExtractResult
	if !isNew && r.Blobs != nil {
		if res, ok, _ := payload.ReadDriverMetadata(r.Blobs, p.ID); ok {
			metadata = &res
		}
	}
	render(w, req, r, "driver_form.html", title, map[string]any{
		"Driver":      p,
		"Filters":     uf,
		"AllowedKeys": allowedKeysList(),
		"IsNew":       isNew,
		"Machines":    machines,
		"Metadata":    metadata,
		"ExtractMsg":  extractMsg,
		"Preview":     preview,
	})
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

		driverFormRender(w, req, r, pkg, false, map[string]any{
			"Identity":       id2,
			"Results":        results,
			"PackageMatches": matches,
		}, "")
	}
}

// driverExtract is the POST /portal/drivers/{id}/extract handler.
// Runs the extraction pipeline (unpacks the zip, scans .inf metadata,
// persists metadata.json) and re-renders the edit page with the
// discovered driver list. The actual work lives in payload.ExtractDriverPackage
// so the same code serves the JSON API endpoint.
func driverExtract(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if _, err := r.Drivers.Get(req.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		res, err := payload.ExtractDriverPackage(req.Context(), r.Drivers, r.Blobs, id)
		if err != nil {
			flash(w, "err", "extract: "+err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", id), http.StatusFound)
			return
		}
		flash(w, "ok", fmt.Sprintf("Extracted %d files (%d .inf entries, %d bytes).",
			res.FileCount, res.INFCount, res.Bytes))
		http.Redirect(w, req, fmt.Sprintf("/portal/drivers/%d/edit", id), http.StatusFound)
	}
}

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
// the system and base-board values are what get written into the new
// filter constraints when the operator picks one — the "Match on"
// selector decides which pair is used.
type machineFilterChoice struct {
	ID                int64
	Label             string
	Manufacturer      string
	Product           string
	BoardManufacturer string
	BoardProduct      string
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
		// A key may carry several values; render one editable row per
		// value so the operator can see and tweak each one. Repeated rows
		// for the same key are regrouped into an array on save.
		parsed, _ := match.ParseFilter(f.FilterJSON)
		u := uiFilter{ID: f.ID}
		for k, vs := range parsed {
			if len(vs) == 0 {
				u.Pairs = append(u.Pairs, uiPair{Key: k, Value: ""})
				continue
			}
			for _, v := range vs {
				u.Pairs = append(u.Pairs, uiPair{Key: k, Value: v})
			}
		}
		uf = append(uf, u)
	}
	title := "New driver package"
	if !isNew {
		title = "Edit driver package: " + p.Name
	}
	// Build the "use machine as filter" dropdown. Only machines whose
	// SMBIOS makes a meaningful filter — system manufacturer + product,
	// or a base-board product — are included; an empty list collapses
	// the helper UI gracefully.
	var machines []machineFilterChoice
	if !isNew && r.Inventory != nil {
		all, _ := r.Inventory.List(req.Context())
		for _, m := range all {
			sysOK := strings.TrimSpace(m.SystemManufacturer) != "" && strings.TrimSpace(m.SystemProduct) != ""
			boardOK := strings.TrimSpace(m.BoardProduct) != ""
			if !sysOK && !boardOK {
				continue
			}
			label := strings.TrimSpace(m.SystemManufacturer + " " + m.SystemProduct)
			if label == "" {
				label = strings.TrimSpace(m.BoardManufacturer+" "+m.BoardProduct) + " (board)"
			}
			machines = append(machines, machineFilterChoice{
				ID:                int64(m.ID),
				Label:             label,
				Manufacturer:      m.SystemManufacturer,
				Product:           m.SystemProduct,
				BoardManufacturer: m.BoardManufacturer,
				BoardProduct:      m.BoardProduct,
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

// filterKey is one selectable filter attribute with a human label so the
// operator recognises it (e.g. "Model" for the raw key system_product).
type filterKey struct {
	Key   string
	Label string
}

// filterKeyLabels maps raw SMBIOS keys to the names operators think in
// (make/model/serial/SKU), big vendors populate these well.
var filterKeyLabels = map[string]string{
	"system_manufacturer": "Make (manufacturer)",
	"system_product":      "Model",
	"system_serial":       "Serial number",
	"system_sku":          "SKU / product code",
	"system_family":       "Product family",
	"system_uuid":         "System UUID",
	"bios_vendor":         "BIOS vendor",
	"bios_version":        "BIOS version",
	"board_manufacturer":  "Board manufacturer",
	"board_product":       "Board model",
	"board_serial":        "Board serial",
}

// allowedKeysList returns the filter keys with friendly labels, ordered
// with the commonly-used identity fields (make/model/serial/SKU) first.
func allowedKeysList() []filterKey {
	order := []string{
		"system_manufacturer", "system_product", "system_serial",
		"system_sku", "system_family", "system_uuid",
		"board_manufacturer", "board_product", "board_serial",
		"bios_vendor", "bios_version",
	}
	out := make([]filterKey, 0, len(match.AllowedKeys))
	seen := map[string]bool{}
	add := func(k string) {
		if !match.AllowedKeys[k] || seen[k] {
			return
		}
		seen[k] = true
		label := filterKeyLabels[k]
		if label == "" {
			label = k
		}
		out = append(out, filterKey{Key: k, Label: label})
	}
	for _, k := range order {
		add(k)
	}
	// Any keys not in the explicit order (future additions) still appear.
	for k := range match.AllowedKeys {
		add(k)
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
		// Group values by key so a key listed several times (e.g. three
		// models sharing this package) becomes one constraint with several
		// acceptable values rather than silently keeping only the last.
		grouped := map[string][]string{}
		var order []string
		for i := 0; i < len(keys) && i < len(vals); i++ {
			k := strings.TrimSpace(keys[i])
			v := strings.TrimSpace(vals[i])
			if k == "" {
				continue
			}
			if _, seen := grouped[k]; !seen {
				order = append(order, k)
			}
			grouped[k] = append(grouped[k], v)
		}
		if len(grouped) == 0 {
			continue
		}
		// Build the filter object. A single value serialises as a bare
		// string (compact and backward compatible); multiple values
		// serialise as an array. Duplicate and empty values are dropped,
		// but a key with only a blank value is kept as a "" wildcard.
		obj := map[string]any{}
		for _, k := range order {
			vs := dedupeNonEmpty(grouped[k])
			switch len(vs) {
			case 0:
				obj[k] = ""
			case 1:
				obj[k] = vs[0]
			default:
				obj[k] = vs
			}
		}
		raw, _ := json.Marshal(obj)
		pkg.Filters = append(pkg.Filters, model.DriverFilter{FilterJSON: string(raw)})
	}
	return pkg, nil
}

// dedupeNonEmpty drops blank entries and case-insensitive duplicates while
// preserving the first-seen order.
func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
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
		if r.Resolver != nil {
			r.Resolver.InvalidateDriverCache()
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
			if r.Resolver != nil {
				r.Resolver.InvalidateDriverCache()
			}
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
			if r.Resolver != nil {
				r.Resolver.InvalidateDriverCache()
			}
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
				if r.Resolver != nil {
					r.Resolver.InvalidateDriverCache()
				}
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
			SystemSKU:          req.FormValue("p_system_sku"),
			SystemFamily:       req.FormValue("p_system_family"),
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

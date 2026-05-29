package portal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

func init() {
	registerSoftwareRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/software", softwareList(r))
		get("/portal/software/new", softwareForm(r, model.SoftwarePackage{}, true))
		post("/portal/software", softwareCreate(r))
		get("/portal/software/{id}/edit", softwareEdit(r))
		post("/portal/software/{id}", softwareUpdate(r))
		post("/portal/software/{id}/delete", softwareDelete(r))
		post("/portal/software/{id}/upload", softwareUpload(r))
		post("/portal/software/{id}/upload/delete", softwareUploadDelete(r))
	}
}

func softwareList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Software.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		refs := map[model.ID]int{}
		for _, p := range v {
			n, _ := r.Software.RefCount(req.Context(), p.ID)
			refs[p.ID] = n
		}
		render(w, req, r, "software_list.html", "Software packages", map[string]any{
			"Packages": v, "Refs": refs,
		})
	}
}

func softwareForm(r Repos, p model.SoftwarePackage, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var rules []swspec.DetectionRule
		var steps []swspec.InstallStep
		if p.DetectionJSON != "" {
			rules, _ = swspec.ParseDetection(p.DetectionJSON)
		}
		if p.StepsJSON != "" {
			steps, _ = swspec.ParseSteps(p.StepsJSON)
		}
		title := "New software package"
		if !isNew {
			title = "Edit software package: " + p.Name
		}
		render(w, req, r, "software_form.html", title, map[string]any{
			"Pkg": p, "Rules": rules, "Steps": steps, "IsNew": isNew,
		})
	}
}

func softwareEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := r.Software.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		softwareForm(r, p, false)(w, req)
	}
}

func buildSoftwareFromForm(req *http.Request) (model.SoftwarePackage, error) {
	if err := req.ParseForm(); err != nil {
		return model.SoftwarePackage{}, err
	}
	pkg := model.SoftwarePackage{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
	}

	// Detection rules. The template emits a list of "rule_index[]" and
	// per-rule fields named rule_<idx>_<field>.
	rules := []swspec.DetectionRule{}
	for _, idx := range req.Form["rule_index[]"] {
		r := swspec.DetectionRule{
			Type:           req.FormValue("rule_" + idx + "_type"),
			FilePath:       req.FormValue("rule_" + idx + "_file_path"),
			FileVersion:    req.FormValue("rule_" + idx + "_file_version"),
			FileSHA256:     req.FormValue("rule_" + idx + "_file_sha256"),
			RegistryHive:   req.FormValue("rule_" + idx + "_registry_hive"),
			RegistryKey:    req.FormValue("rule_" + idx + "_registry_key"),
			RegistryValue:  req.FormValue("rule_" + idx + "_registry_value"),
			RegistryEquals: req.FormValue("rule_" + idx + "_registry_equals"),
			MSIProductCode: req.FormValue("rule_" + idx + "_msi_product_code"),
			ScriptShell:    req.FormValue("rule_" + idx + "_script_shell"),
			ScriptBody:     req.FormValue("rule_" + idx + "_script_body"),
		}
		if err := r.Validate(); err != nil {
			return model.SoftwarePackage{}, fmt.Errorf("detection rule: %w", err)
		}
		rules = append(rules, r)
	}
	rj, _ := json.Marshal(rules)
	pkg.DetectionJSON = string(rj)
	if len(rules) == 0 {
		pkg.DetectionJSON = "[]"
	}

	// Install steps.
	steps := []swspec.InstallStep{}
	for _, idx := range req.Form["step_index[]"] {
		s := swspec.InstallStep{
			Type:              req.FormValue("step_" + idx + "_type"),
			Description:       req.FormValue("step_" + idx + "_desc"),
			SourcePath:        req.FormValue("step_" + idx + "_source_path"),
			DestinationPath:   req.FormValue("step_" + idx + "_destination_path"),
			MSIPath:           req.FormValue("step_" + idx + "_msi_path"),
			APPXPath:          req.FormValue("step_" + idx + "_appx_path"),
			ScriptBody:        req.FormValue("step_" + idx + "_script_body"),
			ExePath:           req.FormValue("step_" + idx + "_exe_path"),
			ContinueOnFailure: req.FormValue("step_"+idx+"_continue") != "",
		}
		if a := req.FormValue("step_" + idx + "_msi_args"); a != "" {
			s.MSIArgs = splitArgs(a)
		}
		if a := req.FormValue("step_" + idx + "_exe_args"); a != "" {
			s.ExeArgs = splitArgs(a)
		}
		if a := req.FormValue("step_" + idx + "_success_codes"); a != "" {
			for _, tok := range strings.Split(a, ",") {
				tok = strings.TrimSpace(tok)
				if tok == "" {
					continue
				}
				n, err := strconv.Atoi(tok)
				if err == nil {
					s.SuccessCodes = append(s.SuccessCodes, n)
				}
			}
		}
		if err := s.Validate(); err != nil {
			return model.SoftwarePackage{}, fmt.Errorf("install step: %w", err)
		}
		steps = append(steps, s)
	}
	sj, _ := json.Marshal(steps)
	pkg.StepsJSON = string(sj)
	if len(steps) == 0 {
		pkg.StepsJSON = "[]"
	}

	return pkg, nil
}

// splitArgs is a tiny POSIX-ish splitter that respects double-quoted
// segments. Good enough for portal-typed install args; complex cases
// can be expressed via cmd / powershell steps.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQ = !inQ
			continue
		}
		if c == ' ' && !inQ {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func softwareCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p, err := buildSoftwareFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/software/new", http.StatusFound)
			return
		}
		out, err := r.Software.Create(req.Context(), p)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/software/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Software package created — upload the installer next.")
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", out.ID), http.StatusFound)
	}
}

func softwareUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		p, err := buildSoftwareFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
			return
		}
		existing, err := r.Software.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		p.ID = id
		// The form doesn't carry payload fields -- those land via
		// the upload / delete-upload handlers. Preserve them so a
		// Save click doesn't blank the displayed filename.
		p.StoragePath = existing.StoragePath
		p.PayloadFilename = existing.PayloadFilename
		p.SizeBytes = existing.SizeBytes
		if err := r.Software.Update(req.Context(), p); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

func softwareDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Software.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/software", http.StatusFound)
	}
}

func softwareUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		pkg, err := r.Software.Get(req.Context(), id)
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
			// part.FileName is the operator's original filename
			// (already path-stripped by net/textproto). Store it
			// for display so the edit page can say
			// "office-365-installer.exe (1.2 GB)" instead of the
			// opaque on-disk blob name.
			origName := part.FileName()
			rel := filepath.ToSlash(filepath.Join("software", fmt.Sprint(int64(id)), "payload.bin"))
			n, err := r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				flash(w, "err", err.Error())
				break
			}
			pkg.StoragePath = rel
			pkg.PayloadFilename = origName
			pkg.SizeBytes = n
			if err := r.Software.Update(req.Context(), pkg); err != nil {
				flash(w, "err", err.Error())
			} else {
				flash(w, "ok", fmt.Sprintf("Uploaded %s (%d bytes).", origName, n))
			}
			break
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

// softwareUploadDelete removes the uploaded installer blob without
// deleting the SoftwarePackage row -- so the detection rules and
// install steps the operator already authored survive a re-upload.
// Returning to the edit page leaves the operator one click away
// from uploading the replacement file.
func softwareUploadDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		pkg, err := r.Software.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if pkg.StoragePath == "" {
			flash(w, "warn", "No installer uploaded; nothing to delete.")
			http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
			return
		}
		// Wipe the blob first; if the FS remove succeeds but the
		// row update fails the operator gets an orphaned row that
		// re-upload will reuse, which is the correct lesser of two
		// evils (vs. an orphaned blob the portal would never show).
		if err := r.Blobs.Remove(pkg.StoragePath); err != nil && !os.IsNotExist(err) {
			flash(w, "err", "Remove blob: "+err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
			return
		}
		oldName := pkg.PayloadFilename
		if oldName == "" {
			oldName = pkg.StoragePath
		}
		pkg.StoragePath = ""
		pkg.PayloadFilename = ""
		pkg.SizeBytes = 0
		if err := r.Software.Update(req.Context(), pkg); err != nil {
			flash(w, "err", "Clear row: "+err.Error())
		} else {
			flash(w, "ok", fmt.Sprintf("Removed installer payload (%s).", oldName))
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

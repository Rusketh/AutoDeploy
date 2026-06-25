package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
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
		// Legacy single-file delete kept so old in-flight forms
		// don't 404; new UI uses per-file delete instead.
		post("/portal/software/{id}/upload/delete", softwareUploadDelete(r))
		// Multi-file: delete one named file from a package.
		post("/portal/software/{id}/files/{name}/delete", softwareFileDelete(r))
		// Bundle zips: uploaded once, extracted into the agent workdir.
		post("/portal/software/{id}/bundle", softwareBundleUpload(r))
		post("/portal/software/{id}/bundle/{name}/delete", softwareBundleDelete(r))
	}
}

// softwarePackageBundleDir returns the BlobStore-relative path under which a
// package's bundle zips live (extracted into the agent workdir at install
// time), kept apart from files/ so the two upload paths can't collide.
func softwarePackageBundleDir(id model.ID) string {
	return filepath.ToSlash(filepath.Join("software", fmt.Sprint(int64(id)), "bundle"))
}

// softwarePackageFilesDir returns the BlobStore-relative path under
// which a package's uploaded files live. Centralised so upload,
// delete, list and the agent-facing manifest can't drift apart.
func softwarePackageFilesDir(id model.ID) string {
	return filepath.ToSlash(filepath.Join("software", fmt.Sprint(int64(id)), "files"))
}

// sanitizeUploadFilename strips path separators, drive letters and
// anything resembling a parent-directory reference from the
// operator-supplied filename. Multipart parts already path-strip on
// most browsers but the spec doesn't require it; the BlobStore
// containment check is a backstop, this is the readable error.
func sanitizeUploadFilename(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("filename is empty")
	}
	// Trim Windows / POSIX leading-path noise the browser might
	// have left in (older IE used to send the full client path).
	for _, sep := range []string{`\`, "/"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+1:]
		}
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("filename resolves to a special path")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("filename contains path separators")
	}
	if len(name) > 255 {
		return "", fmt.Errorf("filename longer than 255 chars")
	}
	return name, nil
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
		compliance, _ := r.Software.AllComplianceSummaries(req.Context())
		render(w, req, r, "software_list.html", "Software packages", map[string]any{
			"Packages": v, "Refs": refs, "Compliance": compliance,
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
		// Multi-file file list. Driven entirely by what's on disk:
		// no separate DB table needed since the filesystem is the
		// source of truth and BlobStore.Resolve already enforces
		// path containment.
		var files []storage.DirEntry
		if !isNew && r.Blobs != nil {
			// Lazy migration: if the package still references a
			// legacy single-file payload.bin but no files dir yet
			// exists, move it under files/<payload_filename> so
			// the new UI shows it. Best-effort; failure is non-
			// fatal (the legacy single-file path is still listed
			// below in a banner so operators aren't blindsided).
			migrateLegacyPayloadIfNeeded(req.Context(), r, &p)
			files, _ = r.Blobs.ListDir(softwarePackageFilesDir(p.ID))
		}
		var bundles []storage.DirEntry
		if !isNew && r.Blobs != nil {
			bundles, _ = r.Blobs.ListDir(softwarePackageBundleDir(p.ID))
		}
		// Other packages, for the dependency picker, plus a set of the
		// currently-selected dependency IDs so the template can mark them.
		all, _ := r.Software.List(req.Context())
		others := make([]model.SoftwarePackage, 0, len(all))
		for _, pk := range all {
			if pk.ID != p.ID {
				others = append(others, pk)
			}
		}
		depSel := make(map[model.ID]bool, len(p.DependsOn))
		for _, d := range p.DependsOn {
			depSel[d] = true
		}
		render(w, req, r, "software_form.html", title, map[string]any{
			"Pkg": p, "Rules": rules, "Steps": steps, "IsNew": isNew,
			"Files": files, "Bundles": bundles, "AllPackages": others, "DepSelected": depSel,
		})
	}
}

// migrateLegacyPayloadIfNeeded moves a pre-multi-file package's
// single payload.bin into the new files/ directory under its
// original filename, then clears the legacy columns. Idempotent: if
// the files dir already has anything, or storage_path is empty, we
// no-op. Errors are swallowed -- the UI falls back to a "legacy
// single-file payload" banner if migration didn't take.
func migrateLegacyPayloadIfNeeded(ctx context.Context, r Repos, p *model.SoftwarePackage) {
	if p.StoragePath == "" {
		return
	}
	filesDir := softwarePackageFilesDir(p.ID)
	if existing, _ := r.Blobs.ListDir(filesDir); len(existing) > 0 {
		// Already migrated, or operator's already on the new path.
		return
	}
	target := p.PayloadFilename
	if target == "" {
		// Pre-migration uploads where part.FileName came up empty.
		// Use a reasonable default so the legacy file is at least
		// visible in the new list.
		target = "payload.bin"
	}
	if _, err := sanitizeUploadFilename(target); err != nil {
		return // refuse to rename into something dodgy
	}
	src, err := r.Blobs.Open(p.StoragePath)
	if err != nil {
		return
	}
	defer src.Close()
	rel := filepath.ToSlash(filepath.Join(filesDir, target))
	if _, err := r.Blobs.WriteStream(rel, src); err != nil {
		return
	}
	// Remove the old single-file blob and clear the legacy columns
	// so subsequent loads skip this whole code path.
	_ = r.Blobs.Remove(p.StoragePath)
	p.StoragePath = ""
	p.PayloadFilename = ""
	p.SizeBytes = 0
	_ = r.Software.Update(ctx, *p)
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
			WingetID:       req.FormValue("rule_" + idx + "_winget_id"),
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
			FilterOS:          strings.TrimSpace(req.FormValue("step_" + idx + "_filter_os")),
			SourcePath:        req.FormValue("step_" + idx + "_source_path"),
			DestinationPath:   req.FormValue("step_" + idx + "_destination_path"),
			MSIPath:           req.FormValue("step_" + idx + "_msi_path"),
			APPXPath:          req.FormValue("step_" + idx + "_appx_path"),
			ScriptBody:        req.FormValue("step_" + idx + "_script_body"),
			ExePath:           req.FormValue("step_" + idx + "_exe_path"),
			WingetID:          req.FormValue("step_" + idx + "_winget_id"),
			ContinueOnFailure: req.FormValue("step_"+idx+"_continue") != "",
		}
		if a := req.FormValue("step_" + idx + "_msi_args"); a != "" {
			s.MSIArgs = splitArgs(a)
		}
		if a := req.FormValue("step_" + idx + "_exe_args"); a != "" {
			s.ExeArgs = splitArgs(a)
		}
		if a := req.FormValue("step_" + idx + "_winget_args"); a != "" {
			s.WingetArgs = splitArgs(a)
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

	// Dependencies: other packages this one requires. Resolving this
	// package will auto-include them (transitively) and install them first.
	// A package can't depend on itself.
	var deps []model.ID
	for _, s := range req.Form["depends_on"] {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 && model.ID(n) != pkg.ID {
			deps = append(deps, model.ID(n))
		}
	}
	pkg.DependsOn = deps

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

// softwareUpload appends a file to the package's files/ directory
// (it does NOT replace previously uploaded files). Operators with
// multi-file installers can upload setup.exe, config.json, drivers/*
// individually and reference each by its filename in install steps.
func softwareUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if _, err := r.Software.Get(req.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mr, err := req.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart upload", http.StatusBadRequest)
			return
		}
		var uploaded int
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
			name, sanErr := sanitizeUploadFilename(part.FileName())
			if sanErr != nil {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				flash(w, "err", "Upload rejected: "+sanErr.Error())
				continue
			}
			rel := filepath.ToSlash(filepath.Join(softwarePackageFilesDir(id), name))
			n, err := r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				flash(w, "err", err.Error())
				break
			}
			uploaded++
			flash(w, "ok", fmt.Sprintf("Uploaded %s (%d bytes).", name, n))
		}
		if uploaded == 0 {
			flash(w, "warn", "No file received in the upload.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

// softwareFileDelete removes one named file from a package's files
// directory. Other files in the package are untouched, the package
// row stays. URL: POST /portal/software/{id}/files/{name}/delete.
func softwareFileDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		raw := req.PathValue("name")
		// PathValue is URL-decoded but we still apply the same
		// sanitiser the upload side uses so a malicious URL can't
		// reach a sibling directory.
		name, sanErr := sanitizeUploadFilename(raw)
		if sanErr != nil {
			flash(w, "err", "Bad filename: "+sanErr.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
			return
		}
		rel := filepath.ToSlash(filepath.Join(softwarePackageFilesDir(id), name))
		if err := r.Blobs.Remove(rel); err != nil && !os.IsNotExist(err) {
			flash(w, "err", "Remove "+name+": "+err.Error())
		} else {
			flash(w, "ok", "Removed "+name+".")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

// softwareBundleUpload stores a zip under the package's bundle/ directory.
// The agent extracts every bundle into its package workdir before running
// steps, so a multi-file installer (e.g. one that lives on a network share
// the SYSTEM account can't reach) can be referenced by bare filename locally.
func softwareBundleUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if _, err := r.Software.Get(req.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mr, err := req.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart upload", http.StatusBadRequest)
			return
		}
		var uploaded int
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
			name, sanErr := sanitizeUploadFilename(part.FileName())
			if sanErr != nil {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				flash(w, "err", "Upload rejected: "+sanErr.Error())
				continue
			}
			if !strings.EqualFold(filepath.Ext(name), ".zip") {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				flash(w, "err", "Bundle must be a .zip file: "+name)
				continue
			}
			rel := filepath.ToSlash(filepath.Join(softwarePackageBundleDir(id), name))
			n, err := r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				flash(w, "err", err.Error())
				break
			}
			uploaded++
			flash(w, "ok", fmt.Sprintf("Uploaded bundle %s (%d bytes).", name, n))
		}
		if uploaded == 0 {
			flash(w, "warn", "No bundle received in the upload.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
	}
}

// softwareBundleDelete removes one named bundle zip from a package.
func softwareBundleDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		name, sanErr := sanitizeUploadFilename(req.PathValue("name"))
		if sanErr != nil {
			flash(w, "err", "Bad filename: "+sanErr.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/software/%d/edit", id), http.StatusFound)
			return
		}
		rel := filepath.ToSlash(filepath.Join(softwarePackageBundleDir(id), name))
		if err := r.Blobs.Remove(rel); err != nil && !os.IsNotExist(err) {
			flash(w, "err", "Remove "+name+": "+err.Error())
		} else {
			flash(w, "ok", "Removed bundle "+name+".")
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

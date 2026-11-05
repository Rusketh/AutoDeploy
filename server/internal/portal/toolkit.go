package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/logging"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// Tool Kits (Build -> Tool Kits): operator-defined, reusable payloads --
// files + ordered steps, the software-package step format -- that run once
// each time they're deployed, on one machine (its page's "Run tool kit"
// action) or many (a bulk operation). Each kit carries a default Run As
// account whose password is encrypted at rest and never rendered back.

func init() {
	registerToolkitRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/toolkits", toolkitList(r))
		get("/portal/toolkits/new", toolkitNew(r))
		post("/portal/toolkits", toolkitCreate(r))
		post("/portal/toolkits/from-software", toolkitFromSoftware(r))
		get("/portal/toolkits/{id}/edit", toolkitEdit(r))
		post("/portal/toolkits/{id}", toolkitUpdate(r))
		post("/portal/toolkits/{id}/delete", toolkitDelete(r))
		post("/portal/toolkits/{id}/duplicate", toolkitDuplicate(r))
		post("/portal/toolkits/{id}/upload", toolkitUpload(r))
		post("/portal/toolkits/{id}/files/delete", toolkitFileDelete(r))
		post("/portal/toolkits/{id}/bundle", toolkitBundleUpload(r))
		post("/portal/toolkits/{id}/bundle/{name}/delete", toolkitBundleDelete(r))
	}
}

func toolkitFilesDir(id model.ID) string {
	return "toolkit/" + strconv.FormatInt(int64(id), 10) + "/files"
}

func toolkitBundleDir(id model.ID) string {
	return "toolkit/" + strconv.FormatInt(int64(id), 10) + "/bundle"
}

func toolkitTarget(id model.ID) string { return "toolkit/" + strconv.FormatInt(int64(id), 10) }

func toolkitEditURL(id model.ID) string {
	return fmt.Sprintf("/portal/toolkits/%d/edit", int64(id))
}

// toolkitsUnavailable renders a 404 when the portal was wired without a
// toolkit repo (tests and minimal builds).
func toolkitsUnavailable(w http.ResponseWriter, req *http.Request, r Repos) bool {
	if r.Toolkits == nil {
		renderPortalError(w, req, r, http.StatusNotFound, "Tool Kits unavailable",
			"This server was started without Tool Kit storage.", "")
		return true
	}
	return false
}

func toolkitList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		all, err := r.Toolkits.List(req.Context())
		if err != nil {
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		search := newSearchBox(req, "Search tool kits", "Search tool kits")
		shown := filterByQuery(all, search.Query, func(k model.Toolkit) []string {
			return []string{k.Name, k.Description, k.RunAsUser}
		})
		stats, _ := r.Toolkits.AllRunStats(req.Context())
		sizes := map[model.ID]int64{}
		if r.Blobs != nil {
			for _, k := range shown {
				var n int64
				entries, _ := r.Blobs.ListTree(toolkitFilesDir(k.ID))
				for _, e := range entries {
					n += e.Size
				}
				bundles, _ := r.Blobs.ListDir(toolkitBundleDir(k.ID))
				for _, e := range bundles {
					n += e.Size
				}
				sizes[k.ID] = n
			}
		}
		pkgs, _ := r.Software.List(req.Context())
		render(w, req, r, "toolkit_list.html", "Tool Kits", map[string]any{
			"Kits": shown, "Stats": stats, "Sizes": sizes, "Packages": pkgs,
			"Search": search.counted(len(shown), len(all), "tool kits"),
		})
	}
}

func toolkitNew(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		toolkitFormPage(w, req, r, model.Toolkit{
			RunAsMode: model.RunAsSystem, RebootPolicy: model.RebootNever,
			TimeoutMinutes: model.DefaultToolkitTimeoutMinutes,
		}, true, nil)
	}
}

func toolkitEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		k, err := r.Toolkits.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		toolkitFormPage(w, req, r, k, false, nil)
	}
}

// toolkitFormPage renders the kit form; with fe set it is a rejected save
// re-rendered with the submitted values (422).
func toolkitFormPage(w http.ResponseWriter, req *http.Request, r Repos, k model.Toolkit, isNew bool, fe *FormError) {
	steps, _ := parseStepsJSON(k.StepsJSON)
	title := "New tool kit"
	if !isNew {
		title = "Edit tool kit: " + k.Name
	}
	var files []*fileNode
	var bundles []storage.DirEntry
	var runs []model.ToolkitRun
	var pendingOps, pendingJobs int
	if !isNew {
		if r.Blobs != nil {
			entries, _ := r.Blobs.ListTree(toolkitFilesDir(k.ID))
			files = buildFileTree(entries)
			bundles, _ = r.Blobs.ListDir(toolkitBundleDir(k.ID))
		}
		runs, _ = r.Toolkits.RunsForToolkit(req.Context(), k.ID, 25)
		pendingOps, pendingJobs, _ = r.Toolkits.PendingUse(req.Context(), k.ID)
		// The stored-password flag comes from the database, not the
		// (possibly re-rendered) submitted form.
		if stored, err := r.Toolkits.Get(req.Context(), k.ID); err == nil {
			k.HasPassword = stored.HasPassword && k.RunAsMode == model.RunAsUser &&
				strings.EqualFold(stored.RunAsUser, k.RunAsUser)
			k.Revision = stored.Revision
		}
	}
	renderForm(w, req, r, "toolkit_form.html", title, fe, map[string]any{
		"Kit": k, "Steps": steps, "IsNew": isNew,
		"Files": files, "Bundles": bundles, "Base": fmt.Sprintf("/portal/toolkits/%d", int64(k.ID)),
		"Runs": runs, "PendingOps": pendingOps, "PendingJobs": pendingJobs,
		"RebootPolicies": []string{model.RebootNever, model.RebootIfRequested, model.RebootAlways},
	})
}

// buildToolkitFromForm reads the kit form. password is the newly typed Run
// As password ("" = keep the stored one). Like the software form it returns
// everything submitted alongside the first error.
func buildToolkitFromForm(req *http.Request) (k model.Toolkit, password string, err error) {
	if err := req.ParseForm(); err != nil {
		return k, "", err
	}
	k = model.Toolkit{
		Name:         strings.TrimSpace(req.FormValue("name")),
		Description:  strings.TrimSpace(req.FormValue("description")),
		RunAsMode:    req.FormValue("run_as_mode"),
		RunAsUser:    strings.TrimSpace(req.FormValue("run_as_user")),
		RebootPolicy: req.FormValue("reboot_policy"),
	}
	password = req.FormValue("run_as_password")
	var firstErr error
	if v := strings.TrimSpace(req.FormValue("timeout_minutes")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			firstErr = fieldErrorf("timeout_minutes", "Enter the timeout as a whole number of minutes (0 = no timeout).")
		}
		k.TimeoutMinutes = n
	}
	// Parameters: param_index[] plus param_<idx>_{name,label,default,required}.
	for _, idx := range req.Form["param_index[]"] {
		name := strings.TrimSpace(req.FormValue("param_" + idx + "_name"))
		if name == "" {
			continue // an untouched blank row
		}
		k.Params = append(k.Params, model.ToolkitParam{
			Name:     name,
			Label:    strings.TrimSpace(req.FormValue("param_" + idx + "_label")),
			Default:  req.FormValue("param_" + idx + "_default"),
			Required: req.FormValue("param_"+idx+"_required") != "",
		})
	}
	steps, serr := parseStepsFromForm(req, true)
	if serr != nil && firstErr == nil {
		firstErr = serr
	}
	sj, _ := json.Marshal(steps)
	k.StepsJSON = string(sj)
	if len(steps) == 0 {
		k.StepsJSON = "[]"
	}
	if k.RunAsMode == model.RunAsUser && firstErr == nil {
		if verr := model.ValidateRunAsUser(k.RunAsUser); verr != nil {
			firstErr = fieldErrorf("run_as_user", "Enter the account as DOMAIN\\user, user@domain or a local account name.")
		}
	}
	return k, password, firstErr
}

// fieldErrorf is a form error tied to one field (reuses the bulk form's
// field-error type so newFormError-style rendering applies).
func fieldErrorf(field, msg string) error { return &bulkFieldErr{Field: field, Msg: msg} }

// toolkitFormError maps a build/save failure onto the kit form.
func toolkitFormError(req *http.Request, err error) *FormError {
	var fieldFail *bulkFieldErr
	if errors.As(err, &fieldFail) {
		return &FormError{
			Message: "Couldn't save — fix the highlighted field below.",
			Fields:  map[string]string{fieldFail.Field: fieldFail.Msg},
		}
	}
	return newFormError(req, model.KindToolkit, err)
}

func toolkitCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		k, pw, err := buildToolkitFromForm(req)
		if err == nil && k.RunAsMode == model.RunAsUser && pw == "" {
			err = fieldErrorf("run_as_password", "Enter the password for "+k.RunAsUser+".")
		}
		if err != nil {
			toolkitFormPage(w, req, r, k, true, toolkitFormError(req, err))
			return
		}
		out, err := r.Toolkits.Create(req.Context(), k, pw)
		if err != nil {
			toolkitFormPage(w, req, r, k, true, toolkitFormError(req, err))
			return
		}
		logToolkitCredentialChange(req, out, pw != "")
		flash(w, "ok", "Tool kit created — upload any files it needs next.")
		http.Redirect(w, req, toolkitEditURL(out.ID), http.StatusFound)
	}
}

func toolkitUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		existing, gerr := r.Toolkits.Get(req.Context(), id)
		if gerr != nil {
			failPage(w, req, r, http.StatusNotFound, gerr)
			return
		}
		k, pw, err := buildToolkitFromForm(req)
		k.ID = id
		// A new account needs its password: the stored one belongs to the
		// previous account.
		if err == nil && k.RunAsMode == model.RunAsUser && pw == "" &&
			(!existing.HasPassword || existing.RunAsMode != model.RunAsUser ||
				!strings.EqualFold(existing.RunAsUser, k.RunAsUser)) {
			err = fieldErrorf("run_as_password", "Enter the password for "+k.RunAsUser+".")
		}
		if err == nil {
			err = r.Toolkits.Update(req.Context(), k, pw)
		}
		if err != nil {
			toolkitFormPage(w, req, r, k, false, toolkitFormError(req, err))
			return
		}
		if pw != "" || (existing.HasPassword && k.RunAsMode != model.RunAsUser) {
			logToolkitCredentialChange(req, k, pw != "")
		}
		ops, jobs, _ := r.Toolkits.PendingUse(req.Context(), id)
		if jobs > 0 || ops > 0 {
			flash(w, "warn", fmt.Sprintf("Saved. %d scheduled or running operation(s) use this tool kit; machines that haven't started yet will run this new revision.", ops))
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
	}
}

// logToolkitCredentialChange records that a kit's Run As password was set or
// cleared -- the fact and the account, never the value.
func logToolkitCredentialChange(req *http.Request, k model.Toolkit, set bool) {
	action := "toolkit.credential.cleared"
	if set {
		action = "toolkit.credential.set"
	}
	logging.Event(req.Context(), action, uploadActor(req), toolkitTarget(k.ID),
		slog.String("account", k.RunAsUser))
}

func toolkitDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		name := nameBeforeDelete(req, r, model.KindToolkit, id)
		err := r.Toolkits.Delete(req.Context(), id)
		if err == nil && r.Blobs != nil {
			_ = r.Blobs.Remove(toolkitTarget(id))
		}
		if errors.Is(err, model.ErrInUse) {
			flash(w, "err", "“"+name+"” is used by a scheduled or running operation. Cancel it on the Bulk page first.")
			http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
			return
		}
		deleteFlash(w, req, model.KindToolkit, name, err)
		http.Redirect(w, req, "/portal/toolkits", http.StatusFound)
	}
}

// toolkitDuplicate copies a kit -- definition, Run As account and stored
// password -- and its files, under "<name> (copy)".
func toolkitDuplicate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		src, err := r.Toolkits.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		var dup model.Toolkit
		for i := 1; i <= 20; i++ {
			name := src.Name + " copy"
			if i > 1 {
				name = fmt.Sprintf("%s copy %d", src.Name, i)
			}
			if len(name) > 128 {
				name = name[len(name)-128:]
			}
			dup, err = r.Toolkits.Duplicate(req.Context(), id, name)
			if !errors.Is(err, model.ErrConflict) {
				break
			}
		}
		if err != nil {
			flash(w, "err", "Couldn't duplicate: "+friendlyError(model.KindToolkit, http.StatusUnprocessableEntity, err))
			http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
			return
		}
		if r.Blobs != nil {
			if cerr := copyBlobTree(r.Blobs, toolkitTarget(id), toolkitTarget(dup.ID)); cerr != nil {
				flash(w, "warn", "Duplicated, but copying its files failed: "+cerr.Error())
				http.Redirect(w, req, toolkitEditURL(dup.ID), http.StatusFound)
				return
			}
		}
		flash(w, "ok", "Duplicated as “"+dup.Name+"”.")
		http.Redirect(w, req, toolkitEditURL(dup.ID), http.StatusFound)
	}
}

// toolkitFromSoftware creates a kit from a software package: its steps and
// files (and bundles) are copied; detection rules are dropped (a kit runs
// every time it's deployed); it runs as SYSTEM, like the package did.
func toolkitFromSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		pid, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64)
		if err != nil || pid <= 0 {
			flash(w, "err", "Choose a software package to copy.")
			http.Redirect(w, req, "/portal/toolkits", http.StatusFound)
			return
		}
		pkg, err := r.Software.Get(req.Context(), model.ID(pid))
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		var kit model.Toolkit
		for i := 1; i <= 20; i++ {
			// Names allow letters, digits, space, dot, dash and underscore.
			name := pkg.Name + " tool kit"
			if i > 1 {
				name = fmt.Sprintf("%s tool kit %d", pkg.Name, i)
			}
			kit, err = r.Toolkits.Create(req.Context(), model.Toolkit{
				Name: name, Description: pkg.Description, StepsJSON: pkg.StepsJSON,
				RunAsMode: model.RunAsSystem, RebootPolicy: model.RebootNever,
				TimeoutMinutes: model.DefaultToolkitTimeoutMinutes,
			}, "")
			if !errors.Is(err, model.ErrConflict) {
				break
			}
		}
		if err != nil {
			flash(w, "err", "Couldn't create the tool kit: "+friendlyError(model.KindToolkit, http.StatusUnprocessableEntity, err))
			http.Redirect(w, req, "/portal/toolkits", http.StatusFound)
			return
		}
		if r.Blobs != nil {
			for _, sub := range []string{"files", "bundle"} {
				src := "software/" + strconv.FormatInt(pid, 10) + "/" + sub
				dst := toolkitTarget(kit.ID) + "/" + sub
				if cerr := copyBlobTree(r.Blobs, src, dst); cerr != nil {
					flash(w, "warn", "Created, but copying the package's files failed: "+cerr.Error())
					http.Redirect(w, req, toolkitEditURL(kit.ID), http.StatusFound)
					return
				}
			}
		}
		flash(w, "ok", "Created “"+kit.Name+"” from the software package. Review its steps and Run As account.")
		http.Redirect(w, req, toolkitEditURL(kit.ID), http.StatusFound)
	}
}

// copyBlobTree copies every file under src to the same relative path under
// dst. A missing src is not an error (nothing to copy).
func copyBlobTree(b *storage.BlobStore, src, dst string) error {
	entries, err := b.ListTree(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		in, err := b.Open(src + "/" + e.Name)
		if err != nil {
			return err
		}
		_, werr := b.WriteStream(dst+"/"+e.Name, in)
		_ = in.Close()
		if werr != nil {
			return werr
		}
	}
	return nil
}

// ---- Files -----------------------------------------------------------------

// bumpToolkitRevision records that a kit's files changed: what a run
// executes changed, so the revision moves on.
func bumpToolkitRevision(req *http.Request, r Repos, id model.ID) {
	_ = r.Toolkits.BumpRevision(req.Context(), id)
}

// toolkitUpload appends files (or a folder, structure preserved) to a kit's
// files/ tree. Mirrors softwareUpload.
func toolkitUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		if _, err := r.Toolkits.Get(req.Context(), id); err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		ctx := req.Context()
		actor, target := uploadActor(req), toolkitTarget(id)
		mr, err := req.MultipartReader()
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, errors.New("expected multipart upload"))
			return
		}
		var uploaded int
		var pendingRel string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				logging.Event(ctx, "toolkit.upload.stream_error", actor, target, slog.String("error", err.Error()))
				flash(w, "err", err.Error())
				break
			}
			if part.FormName() == "relpath" {
				buf, _ := io.ReadAll(io.LimitReader(part, 4096))
				_ = part.Close()
				pendingRel = string(buf)
				continue
			}
			if part.FormName() != "file" {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				continue
			}
			var name string
			var sanErr error
			if strings.TrimSpace(pendingRel) != "" {
				name, sanErr = sanitizeUploadRelPath(pendingRel)
			} else {
				name, sanErr = sanitizeUploadFilename(part.FileName())
			}
			pendingRel = ""
			if sanErr != nil {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				flash(w, "err", "Upload rejected: "+sanErr.Error())
				continue
			}
			rel := filepath.ToSlash(filepath.Join(toolkitFilesDir(id), filepath.FromSlash(name)))
			n, err := r.Blobs.WriteStream(rel, part)
			_ = part.Close()
			if err != nil {
				logging.Event(ctx, "toolkit.upload.write_failed", actor, target,
					slog.String("file", name), slog.String("error", err.Error()))
				flash(w, "err", err.Error())
				break
			}
			uploaded++
			logging.Event(ctx, "toolkit.upload.file_ok", actor, target,
				slog.String("file", name), slog.Int64("bytes", n))
			flash(w, "ok", fmt.Sprintf("Uploaded %s (%d bytes).", name, n))
		}
		if uploaded == 0 {
			flash(w, "warn", "No file received in the upload.")
		} else {
			bumpToolkitRevision(req, r, id)
		}
		http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
	}
}

func toolkitFileDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		name, sanErr := sanitizeUploadRelPath(req.FormValue("name"))
		if sanErr != nil {
			flash(w, "err", "Bad filename: "+sanErr.Error())
			http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
			return
		}
		rel := filepath.ToSlash(filepath.Join(toolkitFilesDir(id), filepath.FromSlash(name)))
		if err := r.Blobs.Remove(rel); err != nil && !os.IsNotExist(err) {
			flash(w, "err", "Remove "+name+": "+err.Error())
		} else {
			bumpToolkitRevision(req, r, id)
			flash(w, "ok", "Removed "+name+".")
		}
		http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
	}
}

func toolkitBundleUpload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		if _, err := r.Toolkits.Get(req.Context(), id); err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		ctx := req.Context()
		actor, target := uploadActor(req), toolkitTarget(id)
		mr, err := req.MultipartReader()
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, errors.New("expected multipart upload"))
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
			if sanErr == nil && !strings.EqualFold(filepath.Ext(name), ".zip") {
				sanErr = fmt.Errorf("bundle must be a .zip file: %s", name)
			}
			if sanErr != nil {
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				flash(w, "err", "Upload rejected: "+sanErr.Error())
				continue
			}
			n, err := r.Blobs.WriteStream(toolkitBundleDir(id)+"/"+name, part)
			_ = part.Close()
			if err != nil {
				logging.Event(ctx, "toolkit.bundle.write_failed", actor, target,
					slog.String("file", name), slog.String("error", err.Error()))
				flash(w, "err", err.Error())
				break
			}
			uploaded++
			logging.Event(ctx, "toolkit.bundle.file_ok", actor, target,
				slog.String("file", name), slog.Int64("bytes", n))
			flash(w, "ok", fmt.Sprintf("Uploaded bundle %s (%d bytes).", name, n))
		}
		if uploaded == 0 {
			flash(w, "warn", "No bundle received in the upload.")
		} else {
			bumpToolkitRevision(req, r, id)
		}
		http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
	}
}

func toolkitBundleDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if toolkitsUnavailable(w, req, r) {
			return
		}
		id, _ := pathID(req)
		name, sanErr := sanitizeUploadFilename(req.PathValue("name"))
		if sanErr != nil {
			flash(w, "err", "Bad filename: "+sanErr.Error())
			http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
			return
		}
		if err := r.Blobs.Remove(toolkitBundleDir(id) + "/" + name); err != nil && !os.IsNotExist(err) {
			flash(w, "err", "Remove "+name+": "+err.Error())
		} else {
			bumpToolkitRevision(req, r, id)
			flash(w, "ok", "Removed bundle "+name+".")
		}
		http.Redirect(w, req, toolkitEditURL(id), http.StatusFound)
	}
}

// parseStepsJSON decodes stored or just-submitted steps WITHOUT validating
// them, so a form rejected for one bad step re-renders every step.
func parseStepsJSON(raw string) ([]swspec.InstallStep, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []swspec.InstallStep
	err := json.Unmarshal([]byte(raw), &out)
	return out, err
}

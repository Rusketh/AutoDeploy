package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerTaskRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/tasks", taskList(r))
		get("/portal/tasks/new", taskFormNew(r))
		post("/portal/tasks", taskCreate(r))
		get("/portal/tasks/{id}/edit", taskEdit(r))
		post("/portal/tasks/{id}", taskUpdate(r))
		post("/portal/tasks/{id}/delete", taskDelete(r))
		post("/portal/tasks/{id}/clone", taskClone(r))
	}
}

func taskList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Tasks.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		refs := map[model.ID]int{}
		for _, t := range v {
			n, _ := r.Tasks.RefCount(req.Context(), t.ID)
			refs[t.ID] = n
		}
		render(w, req, r, "task_list.html", "Scripts & tasks", map[string]any{
			"Tasks": v, "Refs": refs,
		})
	}
}

func taskFormNew(r Repos) http.HandlerFunc {
	return taskFormFor(r, model.Task{PayloadShell: model.TaskShellPowerShell}, true)
}

func taskFormFor(r Repos, t model.Task, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		title := "New script / task"
		if !isNew {
			title = "Edit script / task: " + t.Name
		}
		var codes []int
		if strings.TrimSpace(t.SuccessCodesJSON) != "" {
			_ = json.Unmarshal([]byte(t.SuccessCodesJSON), &codes)
		}
		render(w, req, r, "task_form.html", title, map[string]any{
			"T": t, "IsNew": isNew, "SuccessCodes": codes,
		})
	}
}

func taskEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		t, err := r.Tasks.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		taskFormFor(r, t, false)(w, req)
	}
}

func buildTaskFromForm(req *http.Request) (model.Task, error) {
	if err := req.ParseForm(); err != nil {
		return model.Task{}, err
	}
	t := model.Task{
		Name:              strings.TrimSpace(req.FormValue("name")),
		Description:       strings.TrimSpace(req.FormValue("description")),
		PayloadShell:      strings.TrimSpace(req.FormValue("payload_shell")),
		PayloadBody:       req.FormValue("payload_body"),
		FilterType:        strings.TrimSpace(req.FormValue("filter_type")),
		FilterValue:       req.FormValue("filter_value"),
		ContinueOnFailure: req.FormValue("continue_on_failure") == "1",
	}
	if sc := strings.TrimSpace(req.FormValue("success_codes")); sc != "" {
		// Accept a comma/space separated list like "0, 3010" and store as JSON.
		fields := strings.FieldsFunc(sc, func(r rune) bool { return r == ',' || r == ' ' })
		codes := make([]string, 0, len(fields))
		for _, f := range fields {
			if f == "" {
				continue
			}
			if _, err := strconv.Atoi(f); err != nil {
				return model.Task{}, fmt.Errorf("success codes must be integers: %q", f)
			}
			codes = append(codes, f)
		}
		t.SuccessCodesJSON = "[" + strings.Join(codes, ",") + "]"
	}
	return t, nil
}

func taskCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		t, err := buildTaskFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/tasks/new", http.StatusFound)
			return
		}
		out, err := r.Tasks.Create(req.Context(), t)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/tasks/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Script / task created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/tasks/%d/edit", out.ID), http.StatusFound)
	}
}

func taskUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		t, err := buildTaskFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/tasks/%d/edit", id), http.StatusFound)
			return
		}
		t.ID = id
		if err := r.Tasks.Update(req.Context(), t); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/tasks/%d/edit", id), http.StatusFound)
	}
}

func taskClone(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		src, err := r.Tasks.Get(req.Context(), id)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/tasks", http.StatusFound)
			return
		}
		out, err := r.Tasks.Clone(req.Context(), id, "Copy of "+src.Name)
		if err != nil {
			flash(w, "err", "Clone failed: "+err.Error())
			http.Redirect(w, req, "/portal/tasks", http.StatusFound)
			return
		}
		flash(w, "ok", fmt.Sprintf("Cloned %q — rename and adjust as needed.", src.Name))
		http.Redirect(w, req, fmt.Sprintf("/portal/tasks/%d/edit", out.ID), http.StatusFound)
	}
}

func taskDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Tasks.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/tasks", http.StatusFound)
	}
}

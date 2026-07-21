package portal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerSequenceRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/sequences", sequenceList(r))
		get("/portal/sequences/new", sequenceFormNew(r))
		post("/portal/sequences", sequenceCreate(r))
		get("/portal/sequences/{id}/edit", sequenceEdit(r))
		post("/portal/sequences/{id}", sequenceUpdate(r))
		post("/portal/sequences/{id}/delete", sequenceDelete(r))
		post("/portal/sequences/{id}/clone", sequenceClone(r))
	}
}

// stepEditorPayload is the JSON the client-side step editor consumes. It is
// base64-encoded into a data attribute so no escaping concerns arise in the
// HTML/JS context. Shared by the sequence page and the image-form inline editor.
type stepEditorPayload struct {
	SelfID    int64             `json:"self_id"`
	Steps     []stepEditorStep  `json:"steps"`
	Tasks     []stepEditorNamed `json:"tasks"`
	Sequences []stepEditorNamed `json:"sequences"`
}

type stepEditorStep struct {
	Kind              string `json:"kind"`
	BuiltinAction     string `json:"builtin_action,omitempty"`
	TaskID            int64  `json:"task_id,omitempty"`
	ChildSequenceID   int64  `json:"child_sequence_id,omitempty"`
	ContinueOnFailure bool   `json:"continue_on_failure,omitempty"`
}

type stepEditorNamed struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// buildStepEditorData assembles and base64-encodes the editor payload for a
// sequence with the given steps. selfID is the editing sequence's id (0 for a
// new one) so it can be excluded from the sub-sequence options.
func buildStepEditorData(r Repos, req *http.Request, selfID model.ID, steps []model.SequenceStep) string {
	p := stepEditorPayload{SelfID: int64(selfID)}
	for _, s := range steps {
		es := stepEditorStep{Kind: s.Kind, BuiltinAction: s.BuiltinAction, ContinueOnFailure: s.ContinueOnFailure}
		if s.TaskID != nil {
			es.TaskID = int64(*s.TaskID)
		}
		if s.ChildSequenceID != nil {
			es.ChildSequenceID = int64(*s.ChildSequenceID)
		}
		p.Steps = append(p.Steps, es)
	}
	if tasks, err := r.Tasks.List(req.Context()); err == nil {
		for _, t := range tasks {
			p.Tasks = append(p.Tasks, stepEditorNamed{ID: int64(t.ID), Name: t.Name})
		}
	}
	if seqs, err := r.Sequences.List(req.Context()); err == nil {
		for _, s := range seqs {
			p.Sequences = append(p.Sequences, stepEditorNamed{ID: int64(s.ID), Name: s.Name})
		}
	}
	b, _ := json.Marshal(p)
	return base64.StdEncoding.EncodeToString(b)
}

// parseStepsFromForm reads the client editor's step_json[] parallel array. Each
// entry is one step object; order_value is derived from position (10-spaced so
// there's room to renumber later).
func parseStepsFromForm(req *http.Request) ([]model.SequenceStep, error) {
	raws := req.Form["step_json[]"]
	out := make([]model.SequenceStep, 0, len(raws))
	for i, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var es stepEditorStep
		if err := json.Unmarshal([]byte(raw), &es); err != nil {
			return nil, fmt.Errorf("bad step %d: %v", i+1, err)
		}
		s := model.SequenceStep{
			Kind:              es.Kind,
			BuiltinAction:     es.BuiltinAction,
			OrderValue:        int64((i + 1) * 10),
			ContinueOnFailure: es.ContinueOnFailure,
		}
		if es.TaskID > 0 {
			id := model.ID(es.TaskID)
			s.TaskID = &id
		}
		if es.ChildSequenceID > 0 {
			id := model.ID(es.ChildSequenceID)
			s.ChildSequenceID = &id
		}
		out = append(out, s)
	}
	return out, nil
}

func sequenceList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Sequences.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		refs := map[model.ID]int{}
		for _, s := range v {
			n, _ := r.Sequences.RefCount(req.Context(), s.ID)
			refs[s.ID] = n
		}
		render(w, req, r, "sequence_list.html", "Sequences", map[string]any{
			"Sequences": v, "Refs": refs,
		})
	}
}

func sequenceFormNew(r Repos) http.HandlerFunc {
	return sequenceFormFor(r, model.Sequence{}, true)
}

func sequenceFormFor(r Repos, s model.Sequence, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		title := "New sequence"
		if !isNew {
			title = "Edit sequence: " + s.Name
		}
		render(w, req, r, "sequence_form.html", title, map[string]any{
			"S": s, "IsNew": isNew,
			"EditorData": buildStepEditorData(r, req, s.ID, s.Steps),
		})
	}
}

func sequenceEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		s, err := r.Sequences.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		sequenceFormFor(r, s, false)(w, req)
	}
}

func buildSequenceFromForm(req *http.Request) (model.Sequence, error) {
	if err := req.ParseForm(); err != nil {
		return model.Sequence{}, err
	}
	s := model.Sequence{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
	}
	steps, err := parseStepsFromForm(req)
	if err != nil {
		return model.Sequence{}, err
	}
	s.Steps = steps
	return s, nil
}

func sequenceCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		s, err := buildSequenceFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/sequences/new", http.StatusFound)
			return
		}
		out, err := r.Sequences.Create(req.Context(), s)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/sequences/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Sequence created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/sequences/%d/edit", out.ID), http.StatusFound)
	}
}

func sequenceUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		s, err := buildSequenceFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/sequences/%d/edit", id), http.StatusFound)
			return
		}
		s.ID = id
		if err := r.Sequences.Update(req.Context(), s); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/sequences/%d/edit", id), http.StatusFound)
	}
}

func sequenceClone(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		src, err := r.Sequences.Get(req.Context(), id)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/sequences", http.StatusFound)
			return
		}
		out, err := r.Sequences.Clone(req.Context(), id, "Copy of "+src.Name)
		if err != nil {
			flash(w, "err", "Clone failed: "+err.Error())
			http.Redirect(w, req, "/portal/sequences", http.StatusFound)
			return
		}
		flash(w, "ok", fmt.Sprintf("Cloned %q — rename and adjust as needed.", src.Name))
		http.Redirect(w, req, fmt.Sprintf("/portal/sequences/%d/edit", out.ID), http.StatusFound)
	}
}

func sequenceDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Sequences.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/sequences", http.StatusFound)
	}
}

package portal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func openPortalTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func itoaID(id model.ID) string { return strconv.FormatInt(int64(id), 10) }

// TestParseStepsFromForm: the client editor submits one step_json[] per row;
// the parser decodes them and derives order from position.
func TestParseStepsFromForm(t *testing.T) {
	vals := url.Values{}
	vals.Add("step_json[]", `{"kind":"builtin","builtin_action":"domainjoin"}`)
	vals.Add("step_json[]", `{"kind":"task","task_id":7,"continue_on_failure":true}`)
	vals.Add("step_json[]", `{"kind":"subsequence","child_sequence_id":3}`)
	vals.Add("step_json[]", ``) // blank rows are ignored
	req := httptest.NewRequest("POST", "/x", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	steps, err := parseStepsFromForm(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3: %+v", len(steps), steps)
	}
	if steps[0].Kind != model.SeqStepBuiltin || steps[0].BuiltinAction != model.BuiltinDomainJoin {
		t.Errorf("step 0 = %+v", steps[0])
	}
	if steps[1].Kind != model.SeqStepTask || steps[1].TaskID == nil || *steps[1].TaskID != 7 || !steps[1].ContinueOnFailure {
		t.Errorf("step 1 = %+v", steps[1])
	}
	if steps[2].Kind != model.SeqStepSubsequence || steps[2].ChildSequenceID == nil || *steps[2].ChildSequenceID != 3 {
		t.Errorf("step 2 = %+v", steps[2])
	}
	// Order is derived from position, ascending.
	if !(steps[0].OrderValue < steps[1].OrderValue && steps[1].OrderValue < steps[2].OrderValue) {
		t.Errorf("orders not ascending: %d %d %d", steps[0].OrderValue, steps[1].OrderValue, steps[2].OrderValue)
	}
}

// TestSequenceCreateRoundTrip: posting the sequence form persists the steps
// and they read back in order.
func TestSequenceCreateRoundTrip(t *testing.T) {
	db := openPortalTestDB(t)
	tasks := model.NewTaskRepo(db)
	seqs := model.NewSequenceRepo(db)
	r := Repos{Tasks: tasks, Sequences: seqs}

	tk, err := tasks.Create(context.Background(), model.Task{Name: "Post script", PayloadBody: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}

	vals := url.Values{}
	vals.Set("name", "Full deploy")
	vals.Set("description", "domain join then a script")
	vals.Add("step_json[]", `{"kind":"builtin","builtin_action":"domainjoin"}`)
	vals.Add("step_json[]", `{"kind":"task","task_id":`+itoaID(tk.ID)+`}`)
	req := httptest.NewRequest("POST", "/portal/sequences", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	sequenceCreate(r)(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("create status = %d, want 302; body=%s", w.Code, w.Body.String())
	}

	list, err := seqs.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	got := list[0]
	if len(got.Steps) != 2 ||
		got.Steps[0].BuiltinAction != model.BuiltinDomainJoin ||
		got.Steps[1].Kind != model.SeqStepTask {
		t.Fatalf("persisted steps wrong: %+v", got.Steps)
	}
}

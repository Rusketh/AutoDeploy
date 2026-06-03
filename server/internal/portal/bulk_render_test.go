package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// bulkFuncs is a minimal funcmap covering the helpers the bulk templates use.
func bulkFuncs() template.FuncMap {
	return template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"deref":      func(p *time.Time) time.Time { return *p },
		"formatTime": func(tm time.Time) string { return tm.Format(time.RFC3339) },
		"dict": func(args ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(args); i += 2 {
				k, _ := args[i].(string)
				m[k] = args[i+1]
			}
			return m
		},
	}
}

func renderBulk(t *testing.T, page string, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(bulkFuncs()).ParseFS(assetsFS,
		"templates/"+page, "templates/_action_picker.html")
	if err != nil {
		t.Fatalf("parse %s: %v", page, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec %s: %v", page, err)
	}
	return buf.String()
}

func TestBulkFormRenders(t *testing.T) {
	out := renderBulk(t, "bulk_form.html", map[string]any{
		"Packages":    []model.SoftwarePackage{},
		"Images":      []model.Image{},
		"FormAction":  "/portal/bulk",
		"Editing":     false,
		"AP":          formPrefill(nil),
		"SelInitJSON": "[]",
	})
	for _, want := range []string{`name="action"`, `name="schedule_kind"`, "action-picker", "Recurring", "Run once"} {
		if !strings.Contains(out, want) {
			t.Errorf("bulk_form missing %q", want)
		}
	}
}

func TestBulkListRendersWithProgress(t *testing.T) {
	next := time.Now().Add(time.Hour)
	ops := []model.BulkOperation{{
		ID: 1, Action: "script", Status: model.BulkStatusActive, ScheduleKind: "now",
		CreatedBy: "alice", Progress: model.BulkProgress{Total: 4, OK: 2, Failed: 1, Queued: 1},
	}, {
		ID: 2, Action: "reimage", Status: model.BulkStatusScheduled, ScheduleKind: "once",
		CreatedBy: "bob", NextRunAt: &next,
	}}
	out := renderBulk(t, "bulk_list.html", map[string]any{"Ops": ops})
	for _, want := range []string{"Clear completed", "bulk-bar", "scheduled", "active", "/portal/bulk/2/edit"} {
		if !strings.Contains(out, want) {
			t.Errorf("bulk_list missing %q", want)
		}
	}
}

func TestBulkDetailRenders(t *testing.T) {
	now := time.Now()
	op := model.BulkOperation{
		ID: 5, Action: "script", Status: model.BulkStatusActive, ScheduleKind: "recurring",
		TargetMode: "filter", CreatedBy: "alice", CreatedAt: now, Payload: "{}",
		NextRunAt: &now, Progress: model.BulkProgress{Total: 2, OK: 1, Queued: 1},
	}
	type run struct {
		No   int
		Jobs []model.BulkJob
	}
	out := renderBulk(t, "bulk_detail.html", map[string]any{
		"Op":   op,
		"Jobs": []model.BulkJob{{ID: 1, MachineID: 9, Status: "ok", RunNo: 1}},
		"Runs": []*run{{No: 1, Jobs: []model.BulkJob{{ID: 1, MachineID: 9, Status: "ok", RunNo: 1}}}},
	})
	for _, want := range []string{"data-bulk-progress", "Pause", "Cancel", "re-evaluated each run"} {
		if !strings.Contains(out, want) {
			t.Errorf("bulk_detail missing %q", want)
		}
	}
}

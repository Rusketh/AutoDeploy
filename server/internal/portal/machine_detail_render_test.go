package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// historyRowT mirrors the anonymous struct machineDetail builds: a
// DeploymentRecord plus a derived Stalled flag.
type historyRowT struct {
	model.DeploymentRecord
	Stalled bool
}

// renderMachineDetail parses machine_detail.html with enough of the real
// funcmap and executes its body, returning the HTML. It fails the test on a
// parse or exec error, which is the main guard against template regressions.
func renderMachineDetail(t *testing.T, data map[string]any) string {
	t.Helper()
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"derefID":    func(p *model.ID) int64 { return int64(*p) },
		"deref":      func(p *time.Time) time.Time { return *p },
		"idEq":       func(p *model.ID, v int64) bool { return p != nil && int64(*p) == v },
		"formatTime": func(tm time.Time) string { return tm.Format(time.RFC3339) },
		"relTime":    func(tm time.Time) string { return "just now" },
		"dict": func(args ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(args); i += 2 {
				k, _ := args[i].(string)
				m[k] = args[i+1]
			}
			return m
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS,
		"templates/machine_detail.html", "templates/_action_picker.html")
	if err != nil {
		t.Fatalf("parse machine_detail.html: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec machine_detail.html: %v", err)
	}
	return buf.String()
}

func baseMachineData() map[string]any {
	return map[string]any{
		"M":          model.MachineRecord{ID: 1, SystemUUID: "abcd1234-0000-0000-0000-000000000000", LastSeen: time.Now()},
		"Binding":    model.MachineBinding{MachineID: 1},
		"Images":     []model.Image{},
		"Packages":   []model.SoftwarePackage{},
		"Detected":   nil,
		"KBStatuses": nil,
		"History":    nil,
		"Reimages":   nil,
		"SWDetected": 0, "SWTotal": 0, "KBInstalled": 0, "KBTotal": 0,
		"DeployCount": 0, "MachineStalled": false,
		"LatestDeploy": nil, "LastReimage": nil,
		"DeployActive": false, "DeployLabel": "", "DeployPercent": 0, "DeployIndeterminate": false,
		"ReimagePending": map[string]any{"Pending": false},
		"AP":             formPrefill(nil),
	}
}

// TestMachineDetailReimageBanner: the pending-re-image banner renders only when
// a re-image is flagged, names the target image, and offers a cancel action.
func TestMachineDetailReimageBanner(t *testing.T) {
	t.Run("not pending: no banner", func(t *testing.T) {
		out := renderMachineDetail(t, baseMachineData())
		if strings.Contains(out, "Re-image pending") || strings.Contains(out, "reimage/cancel") {
			t.Errorf("banner must not render when not pending; got:\n%s", out)
		}
	})
	t.Run("pending: banner names target + cancel button", func(t *testing.T) {
		d := baseMachineData()
		d["ReimagePending"] = map[string]any{
			"Pending": true, "Target": "Lab Win11", "Claimed": false, "Since": time.Now(),
		}
		out := renderMachineDetail(t, d)
		if !strings.Contains(out, "Re-image pending") || !strings.Contains(out, "Lab Win11") {
			t.Errorf("want a pending banner naming the target; got:\n%s", out)
		}
		if !strings.Contains(out, "/portal/machines/1/reimage/cancel") || !strings.Contains(out, "Cancel re-image") {
			t.Errorf("want a cancel-re-image form; got:\n%s", out)
		}
	})
	t.Run("failed job: warns the agent couldn't start it", func(t *testing.T) {
		d := baseMachineData()
		d["ReimagePending"] = map[string]any{
			"Pending": true, "Target": "its bound image", "Claimed": false, "Failed": true, "Since": time.Now(),
		}
		out := renderMachineDetail(t, d)
		if !strings.Contains(out, "couldn't be started") {
			t.Errorf("want a failed-agent warning; got:\n%s", out)
		}
	})
}

// TestMachineDetailAtAGlance covers the reworked deployment section: the live
// progress bar when a deploy is active, the at-a-glance summary, and the
// collapsible detail expanders.
func TestMachineDetailAtAGlance(t *testing.T) {
	t.Run("no history", func(t *testing.T) {
		out := renderMachineDetail(t, baseMachineData())
		if !strings.Contains(out, "No deployments recorded yet.") {
			t.Errorf("want empty-history summary; got:\n%s", out)
		}
		if !strings.Contains(out, "Never re-imaged") {
			t.Error("want 'Never re-imaged' in summary")
		}
		// Progress card is present but hidden when no deploy is active.
		if !strings.Contains(out, `card deploy-progress`) || !strings.Contains(out, "hidden") {
			t.Error("want hidden progress card when no active deploy")
		}
	})

	t.Run("active deploy shows progress bar", func(t *testing.T) {
		d := baseMachineData()
		d["DeployActive"] = true
		d["DeployLabel"] = "Installing software"
		d["DeployPercent"] = 80
		out := renderMachineDetail(t, d)
		if !strings.Contains(out, "Installing software") {
			t.Error("want active phase label")
		}
		if !strings.Contains(out, `value="80"`) {
			t.Error("want progress bar value")
		}
	})

	t.Run("history + reimage expanders", func(t *testing.T) {
		now := time.Now()
		iid := model.ID(7)
		d := baseMachineData()
		d["History"] = []historyRowT{{
			DeploymentRecord: model.DeploymentRecord{ID: 1, StartedAt: now, CompletedAt: &now, Outcome: "ok", Phase: "complete", Notes: "done"},
		}}
		d["DeployCount"] = 1
		d["LatestDeploy"] = &model.DeploymentRecord{ID: 1, StartedAt: now, CompletedAt: &now, Outcome: "ok"}
		d["Reimages"] = []model.ReimageEvent{{ID: 1, CreatedAt: now, ImageID: &iid, Source: "remote_flag"}}
		d["LastReimage"] = &model.ReimageEvent{ID: 1, CreatedAt: now, Source: "remote_flag"}
		out := renderMachineDetail(t, d)
		for _, want := range []string{
			"View full deployment history (1)",
			"View re-image history (1)",
			"<details", "</details>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("want %q in output", want)
			}
		}
	})
}

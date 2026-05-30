package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// renderBody parses a single portal page template in isolation and
// executes its {{define "body"}} block with the given Data envelope.
// Enough of the real funcmap is supplied for the ISO pages.
func renderBody(t *testing.T, page string, data map[string]any) string {
	t.Helper()
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"list":       func(args ...any) []any { return args },
		"humanBytes": func(n int64) string { return "X" },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/"+page)
	if err != nil {
		t.Fatalf("parse %s: %v", page, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec %s: %v", page, err)
	}
	return buf.String()
}

func TestISOFormBootMediaStates(t *testing.T) {
	now := time.Now()
	extracted := "iso/1/files/sources/install.swm"
	cases := []struct {
		name     string
		iso      model.ISO
		contains []string
		absent   []string
	}{
		{
			name:     "not extracted",
			iso:      model.ISO{ID: 1, Name: "A", OSType: "windows-11", StoragePath: "iso/1/source.iso"},
			contains: []string{"Not prepared yet", "Extract &amp; prepare"},
		},
		{
			name: "ready, split",
			iso: model.ISO{ID: 1, Name: "A", OSType: "windows-11", StoragePath: extracted,
				InstallImageFormat: "swm", SWMParts: 2, BootloaderPresent: true, MediaPreparedAt: &now},
			contains: []string{"ready to deploy", "split into <strong>2</strong>", "Re-prepare media"},
		},
		{
			name: "missing bootloader",
			iso: model.ISO{ID: 1, Name: "A", OSType: "windows-11", StoragePath: extracted,
				InstallImageFormat: "wim", BootloaderPresent: false, MediaPreparedAt: &now},
			contains: []string{"not UEFI-bootable", "missing"},
			absent:   []string{"ready to deploy"},
		},
		{
			name: "prep error",
			iso: model.ISO{ID: 1, Name: "A", OSType: "windows-11", StoragePath: extracted,
				InstallImageFormat: "wim", BootloaderPresent: true, PrepError: "wimlib split: boom", MediaPreparedAt: &now},
			contains: []string{"needs attention", "wimlib split: boom"},
			absent:   []string{"ready to deploy"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderBody(t, "iso_form.html", map[string]any{"ISO": c.iso, "IsNew": false})
			for _, s := range c.contains {
				if !strings.Contains(out, s) {
					t.Errorf("expected output to contain %q", s)
				}
			}
			for _, s := range c.absent {
				if strings.Contains(out, s) {
					t.Errorf("expected output NOT to contain %q", s)
				}
			}
		})
	}
}

func TestISOListStatusColumn(t *testing.T) {
	now := time.Now()
	isos := []model.ISO{
		{ID: 1, Name: "Ready", OSType: "windows-11", StoragePath: "iso/1/files/sources/install.swm",
			BootloaderPresent: true, SWMParts: 3, MediaPreparedAt: &now},
		{ID: 2, Name: "Broken", OSType: "windows-11", StoragePath: "iso/2/files/sources/install.wim",
			BootloaderPresent: false, MediaPreparedAt: &now},
		{ID: 3, Name: "Fresh", OSType: "windows-11", StoragePath: ""},
	}
	out := renderBody(t, "iso_list.html", map[string]any{
		"ISOs": isos,
		"Refs": map[model.ID]int{1: 0, 2: 0, 3: 0},
	})
	for _, s := range []string{"ready", "split ×3", "not bootable", "not uploaded"} {
		if !strings.Contains(out, s) {
			t.Errorf("iso list missing %q\n%s", s, out)
		}
	}
}

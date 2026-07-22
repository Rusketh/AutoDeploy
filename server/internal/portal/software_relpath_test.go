package portal

import (
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// TestSanitizeUploadRelPath covers the relative-path validator that lets
// folder-structured payloads (e.g. drivers/x.inf) into a package's files/
// tree while still refusing traversal, absolute paths and drive letters.
func TestSanitizeUploadRelPath(t *testing.T) {
	ok := map[string]string{
		"drivers/x.inf":       "drivers/x.inf",
		"a/b/c.dat":           "a/b/c.dat",
		"setup.exe":           "setup.exe",
		`drivers\oem\net.inf`: "drivers/oem/net.inf", // backslashes folded
		"  spaced/name.txt  ": "spaced/name.txt",     // outer space trimmed
	}
	for in, want := range ok {
		got, err := sanitizeUploadRelPath(in)
		if err != nil {
			t.Errorf("sanitizeUploadRelPath(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("sanitizeUploadRelPath(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",                                // empty
		"..",                              // parent
		"../x",                            // traversal
		"a/../b",                          // traversal mid-path
		"/abs/path",                       // absolute
		`C:\x`,                            // drive letter
		"a//b",                            // empty segment
		"a/./b",                           // special segment
		"foo/" + strings.Repeat("x", 256), // over-long segment
	}
	for _, in := range bad {
		if got, err := sanitizeUploadRelPath(in); err == nil {
			t.Errorf("sanitizeUploadRelPath(%q) = %q, want error", in, got)
		}
	}
}

// TestSoftwareFormRendersReorderControls confirms the editor emits move-up /
// move-down buttons for both install steps and detection rules, plus the
// moveBlock helper and a webkitdirectory folder-upload control.
func TestSoftwareFormRendersReorderControls(t *testing.T) {
	out := renderSoftwareForm(t, map[string]any{
		"Pkg":     model.SoftwarePackage{ID: 1, Name: "MyApp"},
		"IsNew":   false,
		"Steps":   []swspec.InstallStep{{Type: "cmd", ScriptBody: "echo hi"}},
		"Rules":   []swspec.DetectionRule{{Type: "winget", WingetID: "Pub.App"}},
		"Files":   []string{},
		"Bundles": []string{},
	})
	for _, want := range []string{
		`onclick="moveBlock(this,-1)"`, // move up (static block)
		`onclick="moveBlock(this,1)"`,  // move down (static block)
		`function moveBlock(`,          // helper present
		`webkitdirectory`,              // folder-upload input
		`data-folder-upload`,           // folder-upload form marker
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered software form missing %q", want)
		}
	}
}

// TestSoftwareFormNestedFileDeleteAction confirms a payload file that lives in
// a sub-folder renders a delete action whose path keeps its slashes intact
// (so the {name...} route matches), not a percent-encoded blob.
func TestSoftwareFormNestedFileDeleteAction(t *testing.T) {
	out := renderSoftwareForm(t, map[string]any{
		"Pkg":     model.SoftwarePackage{ID: 5, Name: "MyApp"},
		"IsNew":   false,
		"Steps":   []swspec.InstallStep{},
		"Rules":   []swspec.DetectionRule{},
		"Files":   []storage.DirEntry{{Name: "drivers/oem.inf", Size: 12, ModTime: time.Now()}},
		"Bundles": []string{},
	})
	if !strings.Contains(out, `/portal/software/5/files/drivers/oem.inf/delete`) {
		t.Errorf("delete action should keep the sub-path slashes intact; got:\n%s", out)
	}
}

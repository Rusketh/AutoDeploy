package portal

import (
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Smoke-test the bulk edit page: form fields present, images listed, and
// the machine search wired to the shared bulk search endpoint.
func TestMachineBulkEditRenders(t *testing.T) {
	out := renderBody(t, "machine_bulk_edit.html", map[string]any{
		"Images": []model.Image{{ID: 1, Name: "Lab Image"}},
	})
	for _, s := range []string{
		"/portal/machines/bulk-edit",
		"/portal/bulk/search",
		"Lab Image",
		"image_mode",
		"machine_name",
		"target_ou",
		"add_groups",
		"remove_groups",
		"%serial(N)%",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("bulk edit page missing %q", s)
		}
	}
}

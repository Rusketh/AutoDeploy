package portal

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestActionPayloadFromForm(t *testing.T) {
	mk := func(vals url.Values) *http.Request {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}

	// Regex find/replace rename (the fleet-rename case).
	p, err := actionPayloadFromForm(mk(url.Values{
		"action": {"rename"}, "rename_find": {"^LAB-A-"}, "rename_replace": {"LAB-B-"},
	}), Repos{})
	if err != nil || !strings.Contains(p, `"rename_find":"^LAB-A-"`) || !strings.Contains(p, `"rename_replace":"LAB-B-"`) {
		t.Errorf("find/replace payload = %q err=%v", p, err)
	}

	// Literal rename takes precedence when both are present.
	p, _ = actionPayloadFromForm(mk(url.Values{
		"action": {"rename"}, "rename_new_name": {"LAB-01"}, "rename_find": {"x"},
	}), Repos{})
	if !strings.Contains(p, `"new_name":"LAB-01"`) {
		t.Errorf("literal rename = %q", p)
	}

	// Software push references a package by id.
	p, err = actionPayloadFromForm(mk(url.Values{
		"action": {"software_push"}, "software_package_id": {"7"},
	}), Repos{})
	if err != nil || !strings.Contains(p, `"package_id":7`) {
		t.Errorf("software push payload = %q err=%v", p, err)
	}

	// Missing required fields error out.
	if _, err := actionPayloadFromForm(mk(url.Values{"action": {"rename"}}), Repos{}); err == nil {
		t.Error("empty rename should error")
	}
	if _, err := actionPayloadFromForm(mk(url.Values{"action": {"software_push"}}), Repos{}); err == nil {
		t.Error("software push without a package should error")
	}
}

package portal

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	}))
	if err != nil || !strings.Contains(p, `"rename_find":"^LAB-A-"`) || !strings.Contains(p, `"rename_replace":"LAB-B-"`) {
		t.Errorf("find/replace payload = %q err=%v", p, err)
	}

	// Literal rename takes precedence when both are present.
	p, _ = actionPayloadFromForm(mk(url.Values{
		"action": {"rename"}, "rename_new_name": {"LAB-01"}, "rename_find": {"x"},
	}))
	if !strings.Contains(p, `"new_name":"LAB-01"`) {
		t.Errorf("literal rename = %q", p)
	}

	// Software push references a package by id.
	p, err = actionPayloadFromForm(mk(url.Values{
		"action": {"software_push"}, "software_package_id": {"7"},
	}))
	if err != nil || !strings.Contains(p, `"package_id":7`) {
		t.Errorf("software push payload = %q err=%v", p, err)
	}

	// Missing required fields error out.
	if _, err := actionPayloadFromForm(mk(url.Values{"action": {"rename"}})); err == nil {
		t.Error("empty rename should error")
	}
	if _, err := actionPayloadFromForm(mk(url.Values{"action": {"software_push"}})); err == nil {
		t.Error("software push without a package should error")
	}
}

func TestBulkFormDeliveryOptions(t *testing.T) {
	mk := func(extra url.Values) *http.Request {
		vals := url.Values{
			"action":      {"script"},
			"script_body": {"echo hi"},
			"machine_ids": {"1"},
		}
		for k, v := range extra {
			vals[k] = v
		}
		req := httptest.NewRequest("POST", "/x", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}

	// Defaults: WoL off, never cancel.
	p, err := parseBulkForm(mk(nil), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if p.WakeOnLAN || p.CancelAfterMinutes != 0 {
		t.Errorf("defaults: wol=%v cancel=%d, want off/0", p.WakeOnLAN, p.CancelAfterMinutes)
	}

	// Hours normalise to minutes; checkbox read.
	p, err = parseBulkForm(mk(url.Values{
		"wake_on_lan": {"1"}, "cancel_after_value": {"6"}, "cancel_after_unit": {"hours"},
	}), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !p.WakeOnLAN || p.CancelAfterMinutes != 360 {
		t.Errorf("wol=%v cancel=%d, want on/360", p.WakeOnLAN, p.CancelAfterMinutes)
	}

	// Minutes pass through.
	p, err = parseBulkForm(mk(url.Values{
		"cancel_after_value": {"90"}, "cancel_after_unit": {"minutes"},
	}), time.UTC)
	if err != nil || p.CancelAfterMinutes != 90 {
		t.Errorf("cancel=%d err=%v, want 90", p.CancelAfterMinutes, err)
	}

	// Garbage rejected with a friendly error.
	if _, err := parseBulkForm(mk(url.Values{
		"cancel_after_value": {"soon"}, "cancel_after_unit": {"hours"},
	}), time.UTC); err == nil {
		t.Error("non-numeric cancel-after should error")
	}
}

// TestParseLocalDateTimeUsesZone: an <input type=datetime-local> value (no
// zone) is interpreted in the supplied zone, so the operator's entered
// wall-clock means the configured zone rather than the server's OS zone.
func TestParseLocalDateTimeUsesZone(t *testing.T) {
	const in = "2026-06-22T14:30"
	utc, err := parseLocalDateTime(in, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	plus5 := time.FixedZone("Z+5", 5*3600)
	got, err := parseLocalDateTime(in, plus5)
	if err != nil {
		t.Fatal(err)
	}
	// Same wall clock, different zones -> instants differ by the offset.
	if d := utc.Sub(got); d != 5*time.Hour {
		t.Errorf("UTC vs +5 interpretation differ by %s, want 5h", d)
	}
	// And the entered wall-clock is preserved when read back in its zone.
	if h := got.In(plus5).Hour(); h != 14 {
		t.Errorf("14:30 entered in +5 reads hour %d, want 14", h)
	}
}

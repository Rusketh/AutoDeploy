package mscatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSearchHTML(t *testing.T) {
	results := parseSearchHTML(readFixture(t, "search_results.html"))
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(results), results)
	}

	r := results[0]
	if r.UpdateID != "234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a" {
		t.Errorf("UpdateID = %q", r.UpdateID)
	}
	if r.Title != "2024-01 Cumulative Update for Windows 10 Version 22H2 for x64-based Systems (KB5034122)" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.KBNumber != "KB5034122" {
		t.Errorf("KBNumber = %q", r.KBNumber)
	}
	if r.Products != "Windows 10, version 1903 and later" {
		t.Errorf("Products = %q", r.Products)
	}
	if r.Classification != "Security Updates" {
		t.Errorf("Classification = %q", r.Classification)
	}
	if r.LastUpdated != "1/9/2024" {
		t.Errorf("LastUpdated = %q", r.LastUpdated)
	}
	if r.SizeText != "634.2 MB" {
		t.Errorf("SizeText = %q", r.SizeText)
	}

	// Second row: server update, "Updates" classification.
	if results[1].KBNumber != "KB5034129" || results[1].SizeText != "328.9 MB" {
		t.Errorf("second row mismatch: %+v", results[1])
	}

	// Third row: a driver with no KB and an HTML entity in the title.
	if results[2].KBNumber != "" {
		t.Errorf("driver row should have no KB, got %q", results[2].KBNumber)
	}
	if results[2].Title != "Intel & Co. - Net - 22.40.0.7" {
		t.Errorf("entity-unescaped title = %q", results[2].Title)
	}
}

func TestParseDownloadLinks(t *testing.T) {
	links := parseDownloadLinks(readFixture(t, "download_dialog.txt"))
	// The fixture repeats the same URL in two JS arrays; dedupe leaves one.
	if len(links) != 1 {
		t.Fatalf("expected 1 deduped link, got %v", links)
	}
	if !strings.HasSuffix(links[0], "windows10.0-kb5034122-x64_3796df3f9e0a9d40bbfb6b13fed4f88b8e1f23bc.msu") {
		t.Errorf("link = %q", links[0])
	}
}

func TestParseKB(t *testing.T) {
	tests := []struct{ title, want string }{
		{"2024-01 Cumulative Update (KB5034122)", "KB5034122"},
		{"Update for Windows (kb5034122)", "KB5034122"},
		{"Update KB 5034122 for Windows", "KB5034122"},
		{"Intel - Net - 22.40.0.7", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ParseKB(tt.title); got != tt.want {
			t.Errorf("ParseKB(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestGuessOSFilter(t *testing.T) {
	tests := []struct{ products, want string }{
		{"Windows 10,  version 1903 and later", "windows-10"},
		{"Windows 11 Client, version 22H2 and later", "windows-11"},
		{"Microsoft Server operating system-21H2, Windows Server 2022", "server-2022"},
		{"Windows Server 2019", "server-2019"},
		{"Windows Server 2025", "server-2025"},
		{"Office 2016", ""},
	}
	for _, tt := range tests {
		if got := GuessOSFilter(tt.products); got != tt.want {
			t.Errorf("GuessOSFilter(%q) = %q, want %q", tt.products, got, tt.want)
		}
	}
}

func TestGuessSeverity(t *testing.T) {
	tests := []struct{ classification, want string }{
		{"Security Updates", "important"},
		{"Critical Updates", "critical"},
		{"Updates", ""},
		{"Drivers (Networking)", ""},
	}
	for _, tt := range tests {
		if got := GuessSeverity(tt.classification); got != tt.want {
			t.Errorf("GuessSeverity(%q) = %q, want %q", tt.classification, got, tt.want)
		}
	}
}

func TestPickFile(t *testing.T) {
	msu := "https://x/win10-kb1-x64.msu"
	cab := "https://x/win10-kb1-x64.cab"
	exe := "https://x/setup.exe"
	tests := []struct {
		links []string
		want  string
	}{
		{[]string{exe, cab, msu}, msu},
		{[]string{exe, cab}, cab},
		{[]string{exe}, exe},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := PickFile(tt.links); got != tt.want {
			t.Errorf("PickFile(%v) = %q, want %q", tt.links, got, tt.want)
		}
	}
}

// End-to-end against an httptest server: asserts the query encoding, the
// DownloadDialog form field, and the parse pipeline.
func TestClientAgainstHTTPTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Search.aspx":
			if got := r.URL.Query().Get("q"); got != "KB5034122 x64" {
				t.Errorf("search q = %q", got)
			}
			w.Write(readFixture(t, "search_results.html"))
		case "/DownloadDialog.aspx":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			ids := r.PostFormValue("updateIDs")
			if !strings.Contains(ids, `"updateID":"234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a"`) {
				t.Errorf("updateIDs form field = %q", ids)
			}
			w.Write(readFixture(t, "download_dialog.txt"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := New()
	c.BaseURL = srv.URL
	ctx := context.Background()

	results, err := c.Search(ctx, "KB5034122 x64")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].KBNumber != "KB5034122" {
		t.Fatalf("search results: %+v", results)
	}

	links, err := c.DownloadLinks(ctx, "234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !strings.HasSuffix(links[0], ".msu") {
		t.Fatalf("links: %v", links)
	}
}

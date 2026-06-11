package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/mscatalog"
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// fakeSearcher implements mscatalog.Searcher without the network.
type fakeSearcher struct {
	results []mscatalog.Result
	links   []string
	err     error
}

func (f *fakeSearcher) Search(ctx context.Context, q string) ([]mscatalog.Result, error) {
	return f.results, f.err
}

func (f *fakeSearcher) DownloadLinks(ctx context.Context, id string) ([]string, error) {
	return f.links, f.err
}

func newCatalogTestServer(t *testing.T, fake *fakeSearcher) (*httptest.Server, Repos) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		Users:     auth.New(db),
		Inventory: inv,
		Updates:   model.NewWindowsUpdateRepo(db, inv),
		Blobs:     blobs,
		MSCatalog: fake,
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

// waitImportDone polls the import-status endpoint until finished.
func waitImportDone(t *testing.T, client *http.Client, url string) payload.ImportStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doJSON(t, client, http.MethodGet, url, nil)
		var s payload.ImportStatus
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		if s.Finished {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import did not finish")
	return payload.ImportStatus{}
}

func TestCatalogSearchEndpoint(t *testing.T) {
	fake := &fakeSearcher{results: []mscatalog.Result{{
		UpdateID: "234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a",
		Title:    "2024-01 Cumulative Update for Windows 10 (KB5034122)",
		KBNumber: "KB5034122",
	}}}
	srv, repos := newCatalogTestServer(t, fake)

	// Unauthenticated -> 401.
	resp, err := http.Get(srv.URL + "/api/v1/mscatalog/search?q=KB5034122")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	client := authedClient(t, srv, repos)

	// Empty query -> 400.
	if r, _ := doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/mscatalog/search?q=", nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("empty q status = %d, want 400", r.StatusCode)
	}

	r, body := doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/mscatalog/search?q=KB5034122", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d body=%s", r.StatusCode, body)
	}
	var out struct {
		Results []mscatalog.Result `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].KBNumber != "KB5034122" {
		t.Errorf("results = %+v", out.Results)
	}
}

func TestUpdateImportHappyPath(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("msu payload bytes"))
	}))
	t.Cleanup(files.Close)
	fake := &fakeSearcher{links: []string{files.URL + "/windows10.0-kb5034122-x64.msu"}}
	srv, repos := newCatalogTestServer(t, fake)
	client := authedClient(t, srv, repos)

	r, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/updates/import", map[string]any{
		"update_guid":    "234C1B3D-6B53-4D11-A4A8-2E8B1AFA1E5A", // case-insensitive
		"title":          "2024-01 Cumulative Update for Windows 10 Version 22H2 (KB5034122)",
		"products":       "Windows 10,  version 1903 and later",
		"classification": "Security Updates",
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d body=%s", r.StatusCode, body)
	}
	var created struct {
		ID model.ID `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	s := waitImportDone(t, client,
		srv.URL+"/api/v1/updates/"+itoaID(created.ID)+"/import-status")
	if s.Phase != payload.ImportDone || s.Error != "" {
		t.Fatalf("import status = %+v", s)
	}

	u, err := repos.Updates.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.KBNumber != "KB5034122" || u.OSFilter != "windows-10" || u.Severity != "important" {
		t.Errorf("metadata = %+v", u)
	}
	if u.PayloadFilename != "windows10.0-kb5034122-x64.msu" || u.SizeBytes != int64(len("msu payload bytes")) {
		t.Errorf("payload fields = %+v", u)
	}
}

func TestUpdateImportDuplicateKB(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("retry bytes"))
	}))
	t.Cleanup(files.Close)
	fake := &fakeSearcher{links: []string{files.URL + "/kb5034122.msu"}}
	srv, repos := newCatalogTestServer(t, fake)
	client := authedClient(t, srv, repos)
	ctx := context.Background()

	body := map[string]any{
		"update_guid": "234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a",
		"title":       "Cumulative Update (KB5034122)",
	}

	// Existing row WITH payload -> 409 + existing_id.
	u, err := repos.Updates.Create(ctx, model.WindowsUpdate{KBNumber: "KB5034122", Title: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Updates.SetPayload(ctx, u.ID, "updates/1/x.msu", "x.msu", 1); err != nil {
		t.Fatal(err)
	}
	r, respBody := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/updates/import", body)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", r.StatusCode, respBody)
	}
	var conflict struct {
		ExistingID model.ID `json:"existing_id"`
	}
	if err := json.Unmarshal(respBody, &conflict); err != nil || conflict.ExistingID != u.ID {
		t.Errorf("conflict body = %s (err %v)", respBody, err)
	}

	// Payloadless row -> reused as a retry (200) and the download fills it.
	if err := repos.Updates.SetPayload(ctx, u.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	r, respBody = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/updates/import", body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d body=%s", r.StatusCode, respBody)
	}
	s := waitImportDone(t, client, srv.URL+"/api/v1/updates/"+itoaID(u.ID)+"/import-status")
	if s.Phase != payload.ImportDone {
		t.Fatalf("retry import = %+v", s)
	}
	got, _ := repos.Updates.Get(ctx, u.ID)
	if got.PayloadFilename != "kb5034122.msu" {
		t.Errorf("retry payload = %+v", got)
	}
}

func TestUpdateImportRejectsBadInput(t *testing.T) {
	srv, repos := newCatalogTestServer(t, &fakeSearcher{})
	client := authedClient(t, srv, repos)

	// No KB in title.
	r, _ := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/updates/import", map[string]any{
		"update_guid": "234c1b3d-6b53-4d11-a4a8-2e8b1afa1e5a",
		"title":       "Intel - Net - 22.40.0.7",
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("no-KB status = %d, want 400", r.StatusCode)
	}

	// Bad GUID.
	r, _ = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/updates/import", map[string]any{
		"update_guid": "not-a-guid",
		"title":       "Update (KB1234567)",
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad-GUID status = %d, want 400", r.StatusCode)
	}
}

func itoaID(id model.ID) string {
	b, _ := json.Marshal(id)
	return string(b)
}

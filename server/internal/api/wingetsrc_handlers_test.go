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
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
	"github.com/rusketh/autodeploy/server/internal/wingetsrc"
)

// fakeWinget implements wingetsrc.Searcher without the network.
type fakeWinget struct {
	results   []wingetsrc.Result
	manifests map[string]*wingetsrc.Manifest
	err       error
}

func (f *fakeWinget) Search(ctx context.Context, q string) ([]wingetsrc.Result, error) {
	return f.results, f.err
}

func (f *fakeWinget) Manifest(ctx context.Context, id, version string) (*wingetsrc.Manifest, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.manifests[id]; ok {
		return m, nil
	}
	return nil, context.Canceled // any error; handler maps to 502
}

func newWingetTestServer(t *testing.T, fake *fakeWinget) (*httptest.Server, Repos) {
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
	repos := Repos{
		Users:    auth.New(db),
		Software: model.NewSoftwarePackageRepo(db),
		Blobs:    blobs,
		Winget:   fake,
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func waitSoftwareImportDone(t *testing.T, client *http.Client, url string) payload.SoftwareImportStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doJSON(t, client, http.MethodGet, url, nil)
		var s payload.SoftwareImportStatus
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		if s.Finished {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("software import did not finish")
	return payload.SoftwareImportStatus{}
}

func TestWingetSearchEndpoint(t *testing.T) {
	fake := &fakeWinget{results: []wingetsrc.Result{
		{PackageIdentifier: "7zip.7zip", Name: "7-Zip", Publisher: "Igor Pavlov", LatestVersion: "23.01"},
	}}
	srv, repos := newWingetTestServer(t, fake)

	// Unauthenticated -> 401.
	resp, err := http.Get(srv.URL + "/api/v1/winget/search?q=7zip")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	client := authedClient(t, srv, repos)
	r, body := doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/winget/search?q=7zip", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d body=%s", r.StatusCode, body)
	}
	var out struct {
		Results []wingetsrc.Result `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].PackageIdentifier != "7zip.7zip" {
		t.Errorf("results = %+v", out.Results)
	}
}

func TestSoftwareImportHappyPath(t *testing.T) {
	installer := []byte("MSI installer bytes")
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(installer)
	}))
	defer files.Close()

	fake := &fakeWinget{
		manifests: map[string]*wingetsrc.Manifest{
			"7zip.7zip": {
				PackageIdentifier: "7zip.7zip",
				Version:           "23.01",
				Name:              "7-Zip",
				Publisher:         "Igor Pavlov",
				Installers: []wingetsrc.Installer{{
					Architecture:  "x64",
					InstallerType: "wix",
					Scope:         "machine",
					URL:           files.URL + "/7z2301-x64.msi",
					ProductCode:   "{23170F69-40C1-2702-2301-000001000000}",
				}},
			},
		},
	}
	srv, repos := newWingetTestServer(t, fake)
	client := authedClient(t, srv, repos)

	r, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/software/import", map[string]string{
		"package_identifier": "7zip.7zip",
		"architecture":       "x64",
		"scope":              "machine",
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d body=%s", r.StatusCode, body)
	}
	var created struct {
		ID   model.ID `json:"id"`
		Name string   `json:"name"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("no package id returned")
	}

	// The package row exists with generated detection + steps.
	pkg, err := repos.Software.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := swspec.ParseSteps(pkg.StepsJSON)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 1 || steps[0].Type != "msi" {
		t.Errorf("steps = %+v", steps)
	}
	rules, err := swspec.ParseDetection(pkg.DetectionJSON)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}
	if len(rules) != 2 || rules[0].Type != "winget" || rules[1].Type != "msi" {
		t.Errorf("detection = %+v", rules)
	}

	// The installer downloads in the background and finishes.
	s := waitSoftwareImportDone(t, client,
		srv.URL+"/api/v1/software/"+itoa(created.ID)+"/import-status")
	if s.Error != "" {
		t.Fatalf("import failed: %s", s.Error)
	}
	if s.Phase != payload.SoftwareImportDone {
		t.Errorf("phase = %s", s.Phase)
	}
	// The downloaded file is under the package's files dir.
	sz, err := repos.Blobs.Size("software/" + itoa(created.ID) + "/files/" + steps[0].MSIPath)
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if sz != int64(len(installer)) {
		t.Errorf("file size = %d, want %d", sz, len(installer))
	}
}

// (itoa is defined in reimage_test.go)

func TestSoftwareImportMissingIdentifier(t *testing.T) {
	srv, repos := newWingetTestServer(t, &fakeWinget{})
	client := authedClient(t, srv, repos)
	r, _ := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/software/import", map[string]string{})
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.StatusCode)
	}
}

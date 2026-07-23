package portal

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newImportRepos(t *testing.T) (Repos, *model.ImportedAssetRepo) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assets := model.NewImportedAssetRepo(db)
	return Repos{ImportedAssets: assets, Logs: model.NewLogRepo(db)}, assets
}

func TestImportUploadCreatesRows(t *testing.T) {
	r, assets := newImportRepos(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("apply_mode", "overwrite")
	fw, _ := mw.CreateFormFile("file", "assets.csv")
	_, _ = fw.Write([]byte("serial,model,name,ou\nSER1,Latitude 5520,PC-01,OU=Sales\nSER2,OptiPlex,PC-02,OU=Eng\n"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/portal/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	importUpload(r)(rec, req)

	if rec.Code != 302 {
		t.Fatalf("want redirect, got %d", rec.Code)
	}
	got, _ := assets.List(context.Background())
	if len(got) != 2 {
		t.Fatalf("want 2 imported rows, got %d", len(got))
	}
	// apply_mode=overwrite is stamped on every row.
	for _, a := range got {
		if !a.Overwrite {
			t.Errorf("row %s should be overwrite mode: %+v", a.Serial, a)
		}
	}
}

func TestImportUploadRejectsMissingFile(t *testing.T) {
	r, assets := newImportRepos(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("apply_mode", "fill")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/portal/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	importUpload(r)(rec, req)

	if rec.Code != 302 {
		t.Fatalf("want redirect, got %d", rec.Code)
	}
	if n, _ := assets.Count(context.Background()); n != 0 {
		t.Errorf("no rows should be created, got %d", n)
	}
	if !hasFlashCookie(rec) {
		t.Error("expected an error flash cookie")
	}
}

func TestImportWipeClearsTable(t *testing.T) {
	r, assets := newImportRepos(t)
	ctx := context.Background()
	_, _, _ = assets.Upsert(ctx, []model.ImportedAsset{
		{Serial: "S1", Model: "M1"}, {Serial: "S2", Model: "M2"},
	})

	req := httptest.NewRequest("POST", "/portal/import/wipe", nil)
	rec := httptest.NewRecorder()
	importWipe(r)(rec, req)

	if rec.Code != 302 {
		t.Fatalf("want redirect, got %d", rec.Code)
	}
	if n, _ := assets.Count(ctx); n != 0 {
		t.Errorf("table not wiped, %d rows remain", n)
	}
}

func TestImportedAssetsTemplateRenders(t *testing.T) {
	now := time.Now()
	assets := []model.ImportedAsset{
		{ID: 1, Serial: "SER1", Model: "Latitude 5520", Name: "PC-01", OU: "OU=Sales", Overwrite: true, MatchedAt: &now},
		{ID: 2, Serial: "SER2", Model: "OptiPlex", Name: "PC-02", OU: "OU=Eng"},
	}
	out := renderBody(t, "imported_assets.html", map[string]any{
		"Assets": assets, "Total": 2, "Matched": 1,
	})
	for _, want := range []string{"SER1", "Latitude 5520", "PC-01", "OU=Sales", "matched", "overwrite", "fill only", "Wipe all"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestImportedAssetsEmptyStateRenders(t *testing.T) {
	out := renderBody(t, "imported_assets.html", map[string]any{
		"Assets": []model.ImportedAsset{}, "Total": 0, "Matched": 0,
	})
	if !strings.Contains(out, "No imported assets yet") {
		t.Errorf("empty state not rendered: %s", out)
	}
}

func hasFlashCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == flashCookieName {
			return true
		}
	}
	return false
}

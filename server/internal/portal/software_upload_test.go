package portal

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestSoftwareUploadRelPath drives the multi-file upload handler with a
// folder upload: each file is preceded by a "relpath" part carrying its
// path relative to the picked folder. The handler must pair each relpath
// with the next file and write it under the package's files/ tree with the
// sub-directories intact, while a plain file part (no relpath) keeps landing
// at its bare basename.
func TestSoftwareUploadRelPath(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	software := model.NewSoftwarePackageRepo(db)
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Software: software, Blobs: blobs}

	pkg, err := software.Create(ctx, model.SoftwarePackage{Name: "MyApp"})
	if err != nil {
		t.Fatal(err)
	}

	// Build a multipart body: one nested file (relpath + file) and one flat
	// file (no relpath, so it falls back to the part basename).
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("relpath", "drivers/oem.inf")
	fw, _ := mw.CreateFormFile("file", "oem.inf")
	_, _ = fw.Write([]byte("[inf-nested]"))
	fw2, _ := mw.CreateFormFile("file", "setup.exe")
	_, _ = fw2.Write([]byte("MZflat"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/portal/software/"+strconv.FormatInt(int64(pkg.ID), 10)+"/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", strconv.FormatInt(int64(pkg.ID), 10))

	rec := httptest.NewRecorder()
	softwareUpload(r)(rec, req)

	// The manifest layer is exercised elsewhere; here assert the bytes
	// landed at the expected on-disk relative paths.
	filesDir := softwarePackageFilesDir(pkg.ID)
	nested, err := blobs.Open(filesDir + "/drivers/oem.inf")
	if err != nil {
		t.Fatalf("nested file not written: %v", err)
	}
	got, _ := io.ReadAll(nested)
	_ = nested.Close()
	if string(got) != "[inf-nested]" {
		t.Errorf("nested content = %q", got)
	}

	flat, err := blobs.Open(filesDir + "/setup.exe")
	if err != nil {
		t.Fatalf("flat file not written: %v", err)
	}
	got2, _ := io.ReadAll(flat)
	_ = flat.Close()
	if string(got2) != "MZflat" {
		t.Errorf("flat content = %q", got2)
	}
}

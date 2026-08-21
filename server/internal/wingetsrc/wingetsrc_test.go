package wingetsrc

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// --- YAML manifest parsing (real fixtures) --------------------------------

func TestParseInstallerYAMLReal7zip(t *testing.T) {
	m, err := parseInstallerYAML(readTestdata(t, "real_7zip.installer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.PackageIdentifier != "7zip.7zip" || m.Version != "23.01" {
		t.Fatalf("identity: %+v", m)
	}
	// The real manifest carries multiple architectures and both exe and wix.
	var haveX64exe, haveWix bool
	for _, inst := range m.Installers {
		if inst.Architecture == "x64" && inst.InstallerType == "exe" {
			haveX64exe = true
			if inst.Silent != "/S" {
				t.Errorf("x64 exe silent = %q, want /S", inst.Silent)
			}
			// Scope is set at the top level (machine) and inherited.
			if inst.Scope != "machine" {
				t.Errorf("x64 exe scope = %q, want machine (inherited)", inst.Scope)
			}
		}
		if inst.InstallerType == "wix" {
			haveWix = true
		}
	}
	if !haveX64exe || !haveWix {
		t.Errorf("expected x64 exe and a wix installer; got %d installers", len(m.Installers))
	}
}

func TestParseInstallerYAMLDepsAndProductCode(t *testing.T) {
	m, err := parseInstallerYAML(readTestdata(t, "synthetic_deps.installer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dependencies) != 1 || m.Dependencies[0] != "Microsoft.VCRedist.2015+.x64" {
		t.Errorf("dependencies = %v", m.Dependencies)
	}
	if len(m.Installers) != 1 {
		t.Fatalf("installers = %d", len(m.Installers))
	}
	inst := m.Installers[0]
	if inst.InstallerType != "inno" || inst.Scope != "machine" {
		t.Errorf("inheritance: type=%q scope=%q", inst.InstallerType, inst.Scope)
	}
	if len(inst.AppsAndFeatures) != 1 || inst.AppsAndFeatures[0].ProductCode != "{11112222-3333-4444-5555-666677778888}" {
		t.Errorf("apps&features = %+v", inst.AppsAndFeatures)
	}
}

func TestApplyLocaleAndDefaultLocale(t *testing.T) {
	loc := parseDefaultLocale(readTestdata(t, "real_7zip.yaml"))
	if loc != "en-US" {
		t.Fatalf("default locale = %q", loc)
	}
	m := &Manifest{}
	applyLocale(m, readTestdata(t, "real_7zip.locale.en-US.yaml"))
	if m.Name != "7-Zip" || m.Publisher != "Igor Pavlov" {
		t.Errorf("locale applied: name=%q publisher=%q", m.Name, m.Publisher)
	}
}

func TestManifestDir(t *testing.T) {
	cases := map[string]struct{ id, version, want string }{
		"simple": {"7zip.7zip", "23.01", "manifests/7/7zip/7zip/23.01"},
		"nested": {"Microsoft.VCRedist.2015+.x64", "14.40", "manifests/m/Microsoft/VCRedist/2015+/x64/14.40"},
	}
	for name, c := range cases {
		if got := manifestDir(c.id, c.version); got != c.want {
			t.Errorf("%s: manifestDir(%q,%q)=%q want %q", name, c.id, c.version, got, c.want)
		}
	}
}

// --- End-to-end Manifest over an httptest raw server ----------------------

func TestClientManifestOverHTTP(t *testing.T) {
	// Serve the real 7zip manifests at the winget-pkgs path layout.
	dir := "/manifests/7/7zip/7zip/23.01/"
	files := map[string][]byte{
		dir + "7zip.7zip.installer.yaml":    readTestdata(t, "real_7zip.installer.yaml"),
		dir + "7zip.7zip.yaml":              readTestdata(t, "real_7zip.yaml"),
		dir + "7zip.7zip.locale.en-US.yaml": readTestdata(t, "real_7zip.locale.en-US.yaml"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), RawBaseURL: srv.URL}
	// version pinned, so the index is never consulted.
	m, err := c.Manifest(context.Background(), "7zip.7zip", "23.01")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "7-Zip" || m.Publisher != "Igor Pavlov" {
		t.Errorf("name/publisher = %q / %q", m.Name, m.Publisher)
	}
	if len(m.Installers) == 0 {
		t.Fatal("no installers parsed")
	}
	// A full import path: pick x64 and build a package.
	chosen := PickInstaller(m.Installers, "x64", "machine")
	if chosen == nil || chosen.Architecture != "x64" {
		t.Fatalf("pick = %+v", chosen)
	}
	bp, err := BuildPackage(m, chosen, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Files) != 1 || len(bp.Steps) != 1 {
		t.Errorf("built = %+v", bp)
	}
}

// --- Index (synthetic source MSIX) ----------------------------------------

// zipAsIndexDB packages a db file as Public/index.db, the winget source
// MSIX layout our extractor reads.
func zipAsIndexDB(t *testing.T, dbPath string) []byte {
	t.Helper()
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("Public/index.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(dbBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildSyntheticIndexV2 writes the v2 source index (as source2.msix ships):
// a single denormalised `packages` table holding one row per package with its
// latest version. Each row is (id, name, moniker, latest_version).
func buildSyntheticIndexV2(t *testing.T, rows [][4]string) []byte {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "src.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror winget-cli's 2.0 PackagesTable: an implicit rowid PK, id/name/
	// latest_version NOT NULL, moniker nullable.
	if _, err := db.Exec(`CREATE TABLE packages(
		rowid INTEGER PRIMARY KEY,
		id TEXT NOT NULL,
		name TEXT NOT NULL,
		moniker TEXT,
		latest_version TEXT NOT NULL,
		arp_min_version TEXT,
		arp_max_version TEXT,
		hash BLOB)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var moniker interface{}
		if r[2] != "" {
			moniker = r[2]
		}
		if _, err := db.Exec(
			`INSERT INTO packages(id,name,moniker,latest_version) VALUES(?,?,?,?)`,
			r[0], r[1], moniker, r[3]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return zipAsIndexDB(t, dbPath)
}

// buildSyntheticIndexV1 writes the v1 source index (as the legacy source.msix
// ships): ids/names/monikers/versions interned by rowid and joined through a
// `manifest` table, one manifest row per package version. Each row is (id,
// name, moniker, version).
func buildSyntheticIndexV1(t *testing.T, rows [][4]string) []byte {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "src.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := []string{
		`CREATE TABLE ids(rowid INTEGER PRIMARY KEY, id TEXT)`,
		`CREATE TABLE names(rowid INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE monikers(rowid INTEGER PRIMARY KEY, moniker TEXT)`,
		`CREATE TABLE versions(rowid INTEGER PRIMARY KEY, version TEXT)`,
		`CREATE TABLE manifest(rowid INTEGER PRIMARY KEY, id INTEGER, name INTEGER, moniker INTEGER, version INTEGER)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	intern := func(table, col, val string) int64 {
		var rowid int64
		err := db.QueryRow("SELECT rowid FROM "+table+" WHERE "+col+"=?", val).Scan(&rowid)
		if err == nil {
			return rowid
		}
		res, err := db.Exec("INSERT INTO "+table+"("+col+") VALUES(?)", val)
		if err != nil {
			t.Fatal(err)
		}
		rowid, _ = res.LastInsertId()
		return rowid
	}
	for _, r := range rows {
		idID := intern("ids", "id", r[0])
		nameID := intern("names", "name", r[1])
		monID := intern("monikers", "moniker", r[2])
		verID := intern("versions", "version", r[3])
		if _, err := db.Exec(
			`INSERT INTO manifest(id,name,moniker,version) VALUES(?,?,?,?)`,
			idID, nameID, monID, verID); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return zipAsIndexDB(t, dbPath)
}

// TestSearchIndexAndVersions exercises the v2 schema served at source2.msix,
// which the client prefers. The v2 index stores one row per package with only
// its latest version.
func TestSearchIndexAndVersions(t *testing.T) {
	msix := buildSyntheticIndexV2(t, [][4]string{
		{"7zip.7zip", "7-Zip", "7zip", "24.08"},
		{"Notepad++.Notepad++", "Notepad++", "notepad++", "8.6.2"},
		{"Google.Chrome", "Google Chrome", "chrome", "120.0"},
	})

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/source2.msix") {
			hits++
			_, _ = w.Write(msix)
			return
		}
		http.NotFound(w, r) // force source.msix path unused
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), IndexBaseURL: srv.URL, CacheDir: t.TempDir()}

	// Search by name substring.
	res, err := c.Search(context.Background(), "7-zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].PackageIdentifier != "7zip.7zip" {
		t.Fatalf("search results = %+v", res)
	}
	if res[0].LatestVersion != "24.08" {
		t.Errorf("latest = %q, want 24.08", res[0].LatestVersion)
	}

	// Moniker match.
	res, err = c.Search(context.Background(), "chrome")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].PackageIdentifier != "Google.Chrome" {
		t.Errorf("moniker search = %+v", res)
	}

	// indexVersions returns the latest version the v2 index carries.
	dbPath, err := c.ensureIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vers, err := c.indexVersions(context.Background(), dbPath, "7zip.7zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 1 || vers[0] != "24.08" {
		t.Errorf("versions = %v, want [24.08]", vers)
	}

	// The index was downloaded once and cached (TTL default is large).
	if hits != 1 {
		t.Errorf("index downloaded %d times, want 1 (cached)", hits)
	}
}

// TestSearchIndexFallbackToSourceMsix exercises the v1 normalised schema via
// the legacy source.msix fallback: source2.msix is absent, so the client
// downloads source.msix and must query its multi-version manifest layout,
// grouping to the newest version.
func TestSearchIndexFallbackToSourceMsix(t *testing.T) {
	msix := buildSyntheticIndexV1(t, [][4]string{
		{"Foo.Bar", "Foo Bar", "foo", "1.0"},
		{"Foo.Bar", "Foo Bar", "foo", "2.0"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// source2.msix missing; only the legacy source.msix is served.
		if strings.HasSuffix(r.URL.Path, "/source.msix") {
			_, _ = w.Write(msix)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), IndexBaseURL: srv.URL, CacheDir: t.TempDir()}
	res, err := c.Search(context.Background(), "foo")
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if len(res) != 1 || res[0].PackageIdentifier != "Foo.Bar" || res[0].LatestVersion != "2.0" {
		t.Errorf("results = %+v", res)
	}

	// The v1 index carries every published version, newest first.
	dbPath, err := c.ensureIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vers, err := c.indexVersions(context.Background(), dbPath, "Foo.Bar")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 2 || vers[0] != "2.0" || vers[1] != "1.0" {
		t.Errorf("versions = %v, want [2.0 1.0]", vers)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := &Client{}
	res, err := c.Search(context.Background(), "   ")
	if err != nil || res != nil {
		t.Errorf("empty query should no-op: res=%v err=%v", res, err)
	}
}

// --- build.go coverage (installer-type -> step/detection mapping) ---------

func mkManifest(id, version, name string, installers ...Installer) *Manifest {
	return &Manifest{PackageIdentifier: id, Version: version, Name: name, Installers: installers}
}

func TestPickInstaller(t *testing.T) {
	insts := []Installer{
		{Architecture: "x64", Scope: "machine", InstallerType: "wix", URL: "u64"},
		{Architecture: "x86", Scope: "machine", InstallerType: "wix", URL: "u86"},
	}
	if got := PickInstaller(insts, "", ""); got == nil || got.Architecture != "x64" {
		t.Fatalf("default pick = %+v", got)
	}
	if got := PickInstaller(insts, "x86", "machine"); got == nil || got.Architecture != "x86" {
		t.Fatalf("x86 pick = %+v", got)
	}
	if got := PickInstaller(insts, "arm64", "machine"); got == nil {
		t.Fatal("arm64 should fall back, not nil")
	}
	if PickInstaller(nil, "", "") != nil {
		t.Error("empty list should pick nil")
	}
}

func TestBuildPackageMSI(t *testing.T) {
	m := mkManifest("7zip.7zip", "23.01", "7-Zip", Installer{
		Architecture: "x64", Scope: "machine", InstallerType: "wix",
		URL:         "https://www.7-zip.org/a/7z2301-x64.msi",
		ProductCode: "{23170F69-40C1-2702-2301-000001000000}",
	})
	bp, err := BuildPackage(m, &m.Installers[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Files) != 1 || !strings.HasSuffix(bp.Files[0].Filename, ".msi") {
		t.Fatalf("files = %+v", bp.Files)
	}
	if len(bp.Steps) != 1 || bp.Steps[0].Type != "msi" || bp.Steps[0].MSIPath != bp.Files[0].Filename {
		t.Fatalf("steps = %+v", bp.Steps)
	}
	if len(bp.Detection) != 2 || bp.Detection[0].Type != "winget" || bp.Detection[1].Type != "msi" {
		t.Fatalf("detection = %+v", bp.Detection)
	}
	if bp.Steps[0].SuccessCodes[1] != 3010 {
		t.Errorf("success codes = %v", bp.Steps[0].SuccessCodes)
	}
}

func TestBuildPackageExeWithPrereqOrdering(t *testing.T) {
	app := mkManifest("Example.InnoApp", "1.2.3", "Inno Example App", Installer{
		Architecture: "x64", Scope: "machine", InstallerType: "inno",
		URL:             "https://downloads.example.com/innoapp/1.2.3/setup-x64.exe",
		AppsAndFeatures: []AppEntry{{ProductCode: "{11112222-3333-4444-5555-666677778888}"}},
	})
	dep := mkManifest("Microsoft.VCRedist.2015+.x64", "14.40", "VC++ Redist", Installer{
		Architecture: "x64", Scope: "machine", InstallerType: "burn",
		URL: "https://aka.ms/vs/17/release/vc_redist.x64.exe",
	})
	bp, err := BuildPackage(app, &app.Installers[0],
		[]DepInstaller{{Manifest: dep, Installer: &dep.Installers[0]}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Files) != 2 || len(bp.Steps) != 2 {
		t.Fatalf("built = files:%d steps:%d", len(bp.Files), len(bp.Steps))
	}
	if bp.Steps[0].Type != "exe" || !strings.Contains(bp.Steps[0].Description, "Prerequisite") {
		t.Errorf("first step should be prereq: %+v", bp.Steps[0])
	}
	if bp.Steps[0].ExeArgs[0] != "/quiet" {
		t.Errorf("burn prereq args = %v", bp.Steps[0].ExeArgs)
	}
	if bp.Steps[1].ExeArgs[0] != "/VERYSILENT" {
		t.Errorf("inno main args = %v", bp.Steps[1].ExeArgs)
	}
	if len(bp.Detection) != 2 || bp.Detection[1].Type != "msi" {
		t.Errorf("detection = %+v", bp.Detection)
	}
}

func TestBuildPackageMSIX(t *testing.T) {
	m := mkManifest("Example.StoreApp", "2.0.0", "Store Example App", Installer{
		Architecture: "x64", InstallerType: "msix",
		URL: "https://downloads.example.com/storeapp/2.0.0/app.msix",
	})
	bp, err := BuildPackage(m, &m.Installers[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Steps) != 1 || bp.Steps[0].Type != "appx" || bp.Steps[0].APPXPath != bp.Files[0].Filename {
		t.Fatalf("steps = %+v", bp.Steps)
	}
	if len(bp.Detection) != 1 || bp.Detection[0].Type != "winget" {
		t.Errorf("detection = %+v", bp.Detection)
	}
}

func TestInstallerFilenameFallback(t *testing.T) {
	m := &Manifest{PackageIdentifier: "Vendor.App", Version: "1.0"}
	name := installerFilename("https://example.com/download?id=42", m, &Installer{InstallerType: "exe"})
	if !strings.HasSuffix(name, ".exe") || !strings.Contains(name, "Vendor-App") {
		t.Errorf("synthesized filename = %q", name)
	}
	name = installerFilename("https://example.com/path/setup.msi", m, &Installer{InstallerType: "msi"})
	if name != "setup.msi" {
		t.Errorf("filename = %q, want setup.msi", name)
	}
}

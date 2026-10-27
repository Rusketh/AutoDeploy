package payload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// exportFixture is an image linked to a deploy-ready ISO whose media tree is
// laid out by the test, backed by an in-memory DB.
type exportFixture struct {
	deps     ISOExportDeps
	blobs    *storage.BlobStore
	imageID  model.ID
	mediaDir string
}

func newExportFixture(t *testing.T, editions []string, unattendJSON string) exportFixture {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isos := model.NewISORepo(db)
	uas := model.NewUnattendRepo(db)
	images := model.NewImageRepo(db)

	iso, err := isos.Create(ctx, model.ISO{Name: "Win11 Lab", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}
	iso.StoragePath = fmt.Sprintf("iso/%d/files/sources/install.wim", int64(iso.ID))
	iso.BootloaderPresent = true
	iso.Editions = editions
	if err := isos.Update(ctx, iso); err != nil {
		t.Fatal(err)
	}
	img := model.Image{Name: "Lab", ISOID: &iso.ID}
	if unattendJSON != "" {
		ua, err := uas.Create(ctx, model.Unattend{Name: "ua", SettingsJSON: unattendJSON})
		if err != nil {
			t.Fatal(err)
		}
		img.UnattendID = &ua.ID
	}
	created, err := images.Create(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	mediaDir, err := blobs.EnsureDir(fmt.Sprintf("iso/%d/files", int64(iso.ID)))
	if err != nil {
		t.Fatal(err)
	}
	return exportFixture{
		deps:     ISOExportDeps{Resolver: resolve.New(images, isos, uas), Blobs: blobs},
		blobs:    blobs,
		imageID:  created.ID,
		mediaDir: mediaDir,
	}
}

// windowsLikeMedia lays out an upper-case Windows-style tree, including an
// answer file the source ISO shipped with (which the export must replace).
func windowsLikeMedia(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "BOOT", "ETFSBOOT.COM"), string(recognisableBytes("ETFSBOOT", 4096)))
	mustWrite(t, filepath.Join(dir, "EFI", "MICROSOFT", "BOOT", "EFISYS.BIN"), strings.Repeat("u", 4096))
	mustWrite(t, filepath.Join(dir, "EFI", "MICROSOFT", "BOOT", "EFISYS_NOPROMPT.BIN"), string(recognisableBytes("NOPROMPT", 4096)))
	mustWrite(t, filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI"), strings.Repeat("e", 4096))
	mustWrite(t, filepath.Join(dir, "SOURCES", "BOOT.WIM"), "wim")
	mustWrite(t, filepath.Join(dir, "SOURCES", "INSTALL.SWM"), "swm")
	mustWrite(t, filepath.Join(dir, "AutoUnattend.xml"), "<shipped-with-iso/>")
}

func requireISOBuilder(t *testing.T) {
	t.Helper()
	if !ISOBuilderAvailable() {
		t.Skip("xorriso not installed (ExportImageISO checks for it up front)")
	}
}

func TestPreflightExport_Edition(t *testing.T) {
	ctx := context.Background()
	editions := []string{"Windows 11 Education", "Windows 11 Pro"}

	// The unattend names an edition the ISO lacks: clear, actionable error.
	f := newExportFixture(t, editions, `{"edition":"Windows 11 Enterprise"}`)
	_, err := f.deps.PreflightExport(ctx, f.imageID)
	var pe *ExportPreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("want ExportPreflightError, got %v", err)
	}
	for _, want := range []string{`"Windows 11 Enterprise"`, "Windows 11 Education, Windows 11 Pro", "Win11 Lab"} {
		if !strings.Contains(pe.Msg, want) {
			t.Errorf("message missing %q: %s", want, pe.Msg)
		}
	}

	// Edition unset defaults to Windows 11 Pro; the message must say so when
	// the ISO lacks it.
	f = newExportFixture(t, []string{"Windows 11 Home"}, `{"edition":""}`)
	if _, err := f.deps.PreflightExport(ctx, f.imageID); err == nil || !strings.Contains(err.Error(), "defaults to Windows 11 Pro") {
		t.Errorf("unset edition: got %v", err)
	}

	// Match is case-insensitive (Setup's /IMAGE/NAME match is too).
	f = newExportFixture(t, editions, `{"edition":"windows 11 pro"}`)
	if _, err := f.deps.PreflightExport(ctx, f.imageID); err != nil {
		t.Errorf("case-insensitive match rejected: %v", err)
	}

	// No unattend at all: defaults (Windows 11 Pro) — present, so fine.
	f = newExportFixture(t, editions, "")
	if _, err := f.deps.PreflightExport(ctx, f.imageID); err != nil {
		t.Errorf("default edition rejected: %v", err)
	}

	// ISOs prepared by an older server recorded no editions: not checked.
	f = newExportFixture(t, nil, `{"edition":"Anything"}`)
	if _, err := f.deps.PreflightExport(ctx, f.imageID); err != nil {
		t.Errorf("ISO without recorded editions must not be rejected: %v", err)
	}
}

func TestPreflightExport_NotReady(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, _ := storage.NewBlobStore(t.TempDir())
	isos, uas, images := model.NewISORepo(db), model.NewUnattendRepo(db), model.NewImageRepo(db)
	deps := ISOExportDeps{Resolver: resolve.New(images, isos, uas), Blobs: blobs}

	noISO, _ := images.Create(ctx, model.Image{Name: "bare"})
	var pe *ExportPreflightError
	if _, err := deps.PreflightExport(ctx, noISO.ID); !errors.As(err, &pe) || !strings.Contains(pe.Msg, "link no ISO") {
		t.Errorf("no ISO: got %v", err)
	}

	iso, _ := isos.Create(ctx, model.ISO{Name: "raw", OSType: "windows-11"})
	img, _ := images.Create(ctx, model.Image{Name: "unprepared", ISOID: &iso.ID})
	if _, err := deps.PreflightExport(ctx, img.ID); !errors.As(err, &pe) || !strings.Contains(pe.Msg, "not ready") {
		t.Errorf("unprepared ISO: got %v", err)
	}
}

func TestMediaCaseRel(t *testing.T) {
	media := t.TempDir()
	mustWrite(t, filepath.Join(media, "SOURCES", "$oem$", "x"), "")
	mustWrite(t, filepath.Join(media, "AutoUnattend.xml"), "")
	cases := map[string]string{
		"sources/$OEM$/$$/Setup":   filepath.Join("SOURCES", "$oem$", "$$", "Setup"),
		"autounattend.xml":         "AutoUnattend.xml",
		"$WinPEDriver$":            "$WinPEDriver$", // absent: keep our spelling
		"AutoDeploy/diskguard.cmd": filepath.Join("AutoDeploy", "diskguard.cmd"),
	}
	for in, want := range cases {
		if got := mediaCaseRel(media, in); got != want {
			t.Errorf("mediaCaseRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportImageISO_OverlayFollowsMediaCase(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, []string{"Windows 11 Pro"}, "")
	windowsLikeMedia(t, f.mediaDir)
	agent := filepath.Join(t.TempDir(), "agent.exe")
	mustWrite(t, agent, "MZ")

	rr := &recordingRunner{}
	f.deps.Runner = rr
	work := t.TempDir()
	if err := f.deps.ExportImageISO(context.Background(), ISOExportOptions{
		ImageID: f.imageID, BaseURL: "http://10.0.0.5:8080", OutPath: filepath.Join(t.TempDir(), "o.iso"),
		AgentBinaryPath: agent, WorkDir: work,
	}); err != nil {
		t.Fatalf("ExportImageISO: %v", err)
	}
	overlay := filepath.Join(work, "overlay")
	for _, want := range []string{
		"AutoUnattend.xml", // replaces the media's own answer file
		filepath.Join("SOURCES", "$OEM$", "$$", "AutoDeploy", "autodeploy-agent.exe"),
		filepath.Join("SOURCES", "$OEM$", "$$", "Setup", "Scripts", "SetupComplete.cmd"),
		filepath.Join("AutoDeploy", "diskguard.cmd"),
	} {
		if _, err := os.Stat(filepath.Join(overlay, want)); err != nil {
			t.Errorf("overlay missing %s", want)
		}
	}
	for _, dup := range []string{"autounattend.xml", "sources"} {
		if _, err := os.Stat(filepath.Join(overlay, dup)); err == nil {
			t.Errorf("overlay has %q next to the media's own casing (would duplicate in the ISO)", dup)
		}
	}
	xml, _ := os.ReadFile(filepath.Join(overlay, "AutoUnattend.xml"))
	if !strings.Contains(string(xml), "diskguard.cmd") || strings.Contains(string(xml), "<DiskConfiguration>") {
		t.Error("exported answer file must use the disk guard, not a static DiskConfiguration")
	}
	joined := strings.Join(rr.args, " ")
	for _, want := range []string{"-b BOOT/ETFSBOOT.COM", "-e EFI/MICROSOFT/BOOT/EFISYS_NOPROMPT.BIN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("xorriso args missing %q: %s", want, joined)
		}
	}
}

func TestExportImageISO_RefusesUnbootableMedia(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	mustWrite(t, filepath.Join(f.mediaDir, "sources", "boot.wim"), "wim")
	mustWrite(t, filepath.Join(f.mediaDir, "efi", "boot", "bootx64.efi"), "e")
	rr := &recordingRunner{}
	f.deps.Runner = rr
	err := f.deps.ExportImageISO(context.Background(), ISOExportOptions{
		ImageID: f.imageID, OutPath: filepath.Join(t.TempDir(), "o.iso"), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no boot images") {
		t.Fatalf("want a no-boot-images error, got %v", err)
	}
	if rr.name != "" {
		t.Error("xorriso must not run for media with no boot image")
	}
}

func TestExportImageISO_WarnsOnSingleFirmware(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	mustWrite(t, filepath.Join(f.mediaDir, "efi", "microsoft", "boot", "efisys.bin"), "u")
	f.deps.Runner = &recordingRunner{}
	var warns []string
	if err := f.deps.ExportImageISO(context.Background(), ISOExportOptions{
		ImageID: f.imageID, OutPath: filepath.Join(t.TempDir(), "o.iso"), WorkDir: t.TempDir(),
		Warn: func(m string) { warns = append(warns, m) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "UEFI machines only") {
		t.Errorf("warnings = %q", warns)
	}
}

// fakeISORunner stands in for xorriso: it writes a minimal ISO9660-looking
// file to the -o path (or garbage / nothing), optionally blocking until the
// context is cancelled.
type fakeISORunner struct {
	mu      sync.Mutex
	outPath string
	content []byte
	err     error
	block   bool
	started chan struct{}
}

func (r *fakeISORunner) Exec(ctx context.Context, _ string, args ...string) error {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			r.mu.Lock()
			r.outPath = args[i+1]
			r.mu.Unlock()
			if r.content != nil {
				if err := os.WriteFile(args[i+1], r.content, 0o644); err != nil {
					return err
				}
			}
		}
	}
	if r.started != nil {
		close(r.started)
	}
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.err
}

func fakeISOBytes() []byte {
	b := make([]byte, 0x9000)
	copy(b[0x8001:], "CD001")
	return b
}

func waitExport(t *testing.T, id model.ID) ExportStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := ExportStatusFor(id); s.Finished {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("export did not finish")
	return ExportStatus{}
}

func TestStartExportAsync_WritesPartialThenRenames(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	windowsLikeMedia(t, f.mediaDir)
	runner := &fakeISORunner{content: fakeISOBytes()}
	f.deps.Runner = runner
	final, err := ExportOutputPath(f.blobs, f.imageID)
	if err != nil {
		t.Fatal(err)
	}
	// A stale partial from an interrupted run must not get in the way.
	mustWrite(t, final+".partial", "stale")

	if !StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final}) {
		t.Fatal("export did not start")
	}
	st := waitExport(t, f.imageID)
	if st.Phase != ExportDone {
		t.Fatalf("phase = %s (%s)", st.Phase, st.Error)
	}
	if runner.outPath != final+".partial" {
		t.Errorf("xorriso wrote to %q, want the .partial path", runner.outPath)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Error("partial file left behind after success")
	}
	if fi, ok := FinishedExport(final); !ok || fi.Size() != st.SizeBytes || st.BuiltAt == nil {
		t.Errorf("final ISO not in place / size mismatch: %+v", st)
	}
}

func TestStartExportAsync_FailureKeepsPreviousISO(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	windowsLikeMedia(t, f.mediaDir)
	final, _ := ExportOutputPath(f.blobs, f.imageID)
	previous := fakeISOBytes()
	previous[0] = 'P'
	if err := os.WriteFile(final, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	// xorriso writes half an image then fails.
	f.deps.Runner = &fakeISORunner{content: []byte("half an iso"), err: errors.New("exit status 5: disk full")}
	StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final})
	st := waitExport(t, f.imageID)
	if st.Phase != ExportError || !strings.Contains(st.Error, "disk full") {
		t.Fatalf("status = %+v", st)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Error("failed partial not removed")
	}
	got, _ := os.ReadFile(final)
	if len(got) == 0 || got[0] != 'P' {
		t.Error("previous good ISO was clobbered by a failed export")
	}
}

func TestStartExportAsync_RejectsNonISOOutput(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	windowsLikeMedia(t, f.mediaDir)
	final, _ := ExportOutputPath(f.blobs, f.imageID)
	f.deps.Runner = &fakeISORunner{content: []byte("not an iso")}
	StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final})
	if st := waitExport(t, f.imageID); st.Phase != ExportError {
		t.Fatalf("garbage output accepted: %+v", st)
	}
	if _, ok := FinishedExport(final); ok {
		t.Error("garbage output was moved into place")
	}
}

func TestStartExportAsync_Cancel(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, nil, "")
	windowsLikeMedia(t, f.mediaDir)
	final, _ := ExportOutputPath(f.blobs, f.imageID)
	runner := &fakeISORunner{content: []byte("partial"), block: true, started: make(chan struct{})}
	f.deps.Runner = runner
	if !StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final}) {
		t.Fatal("export did not start")
	}
	<-runner.started
	// A second start while running is a no-op.
	if StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final}) {
		t.Error("second concurrent export started")
	}
	if !CancelExport(f.imageID) {
		t.Fatal("CancelExport found nothing running")
	}
	st := waitExport(t, f.imageID)
	if st.Phase != ExportError || !strings.Contains(st.Error, "cancelled") {
		t.Fatalf("status = %+v", st)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Error("cancelled partial not removed")
	}
	if CancelExport(f.imageID) {
		t.Error("cancelling a finished export reported true")
	}
}

func TestStartExportAsync_Timeout(t *testing.T) {
	requireISOBuilder(t)
	old := ExportTimeout
	ExportTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ExportTimeout = old })
	f := newExportFixture(t, nil, "")
	windowsLikeMedia(t, f.mediaDir)
	final, _ := ExportOutputPath(f.blobs, f.imageID)
	f.deps.Runner = &fakeISORunner{block: true}
	StartExportAsync(f.deps, ISOExportOptions{ImageID: f.imageID, OutPath: final})
	if st := waitExport(t, f.imageID); st.Phase != ExportError || !strings.Contains(st.Error, "timed out") {
		t.Fatalf("status = %+v", st)
	}
}

func TestFinishedExport(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.iso")
	if err := os.WriteFile(good, fakeISOBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := FinishedExport(good); !ok {
		t.Error("valid ISO rejected")
	}
	empty := filepath.Join(dir, "empty.iso")
	_ = os.WriteFile(empty, nil, 0o644)
	short := filepath.Join(dir, "short.iso")
	_ = os.WriteFile(short, []byte("x"), 0o644)
	for _, p := range []string{empty, short, filepath.Join(dir, "missing.iso"), dir} {
		if _, ok := FinishedExport(p); ok {
			t.Errorf("FinishedExport(%s) accepted", p)
		}
	}
}

// TestExportImageISO_RealXorriso is the end-to-end regression guard: a full
// export of a Windows-like, upper-case media tree through the REAL xorriso,
// then inspection of the resulting ISO — dual El Torito entries, an unpatched
// etfsboot.com, and ONE answer file / ONE sources dir (the overlay merged into
// the media's casing instead of duplicating it).
func TestExportImageISO_RealXorriso(t *testing.T) {
	requireISOBuilder(t)
	f := newExportFixture(t, []string{"Windows 11 Pro"}, "")
	windowsLikeMedia(t, f.mediaDir)
	agent := filepath.Join(t.TempDir(), "agent.exe")
	mustWrite(t, agent, "MZ")
	final, _ := ExportOutputPath(f.blobs, f.imageID)
	if !StartExportAsync(f.deps, ISOExportOptions{
		ImageID: f.imageID, BaseURL: "http://10.0.0.5:8080", OutPath: final, AgentBinaryPath: agent,
	}) {
		t.Fatal("export did not start")
	}
	st := waitExport(t, f.imageID)
	if st.Phase != ExportDone {
		t.Fatalf("export failed: %s", st.Error)
	}

	report := xorrisoOut(t, "-indev", final, "-report_el_torito", "plain")
	for _, want := range []string{"BIOS  y   none", "UEFI  y   none", "/BOOT/ETFSBOOT.COM", "/EFI/MICROSOFT/BOOT/EFISYS_NOPROMPT.BIN"} {
		if !strings.Contains(report, want) {
			t.Errorf("El Torito report missing %q\n%s", want, report)
		}
	}
	if got := xorrisoExtract(t, final, "/BOOT/ETFSBOOT.COM"); string(got) != string(recognisableBytes("ETFSBOOT", 4096)) {
		t.Errorf("etfsboot.com modified in the ISO (first diff at byte %d)", firstDiff(got, recognisableBytes("ETFSBOOT", 4096)))
	}

	count := func(names []string, want string) int {
		n := 0
		for _, nm := range names {
			if strings.EqualFold(nm, want) {
				n++
			}
		}
		return n
	}
	root := xorrisoRootNames(t, final, "/")
	for _, name := range []string{"autounattend.xml", "sources", "AutoDeploy"} {
		if c := count(root, name); c != 1 {
			t.Errorf("ISO root has %d entries matching %q (want exactly 1): %q", c, name, root)
		}
	}
	ua := string(xorrisoExtract(t, final, "/AutoUnattend.xml"))
	if strings.Contains(ua, "shipped-with-iso") || !strings.Contains(ua, "diskguard.cmd") {
		t.Errorf("the ISO's answer file is not the generated, guarded one:\n%s", ua)
	}
	if c := count(xorrisoRootNames(t, final, "/SOURCES"), "$OEM$"); c != 1 {
		t.Errorf("SOURCES has %d $OEM$ dirs, want 1", c)
	}
	if guard := string(xorrisoExtract(t, final, "/AutoDeploy/diskguard.cmd")); !strings.Contains(guard, "wpeutil shutdown") {
		t.Error("disk guard script missing from the ISO")
	}
}

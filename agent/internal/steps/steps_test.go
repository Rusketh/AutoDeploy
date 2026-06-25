package steps

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

func TestExecuteAllStepTypes(t *testing.T) {
	rec := &Recorder{}
	list := []swspec.InstallStep{
		{Type: "copy", SourcePath: "src.dat", DestinationPath: "dst.dat"},
		{Type: "msi", MSIPath: `C:\pkg\a.msi`, MSIArgs: []string{"TARGETDIR=C:\\Foo"}},
		{Type: "appx", APPXPath: `C:\pkg\a.appx`},
		{Type: "cmd", ScriptBody: `echo hi`},
		{Type: "powershell", ScriptBody: `Write-Host hi`},
		{Type: "exe", ExePath: `C:\pkg\a.exe`, ExeArgs: []string{"/S"}},
	}
	res := Execute(context.Background(), list, rec, HostFacts{})
	if len(res) != 6 {
		t.Fatalf("expected 6 results, got %d", len(res))
	}
	for i, r := range res {
		if r.Aborted {
			t.Errorf("result %d aborted unexpectedly: %+v", i, r)
		}
	}
	mustCall := []string{
		"msiexec /i C:\\pkg\\a.msi /quiet /norestart TARGETDIR=C:\\Foo",
		"powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command Add-AppxPackage",
		"cmd (script) echo hi",
		"powershell (script) Write-Host hi",
		`C:\pkg\a.exe /S`,
	}
	for _, want := range mustCall {
		found := false
		for _, c := range rec.Calls {
			if strings.Contains(c, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected call containing %q\n%s", want, rec.Dump())
		}
	}
	if len(rec.Copies) != 1 || !strings.Contains(rec.Copies[0], "src.dat -> dst.dat") {
		t.Errorf("expected copy src.dat -> dst.dat\n%s", rec.Dump())
	}
}

// cmd/powershell steps write the body to a real script file (so multi-line
// bodies survive -- an inline `cmd /C <body>` only runs the first line). The
// file-writing helper normalises to CRLF and keeps the requested extension.
func TestWriteScriptFileCRLF(t *testing.T) {
	dir := t.TempDir()
	path, err := writeScriptFile(dir, ".cmd", "echo one\necho two\r\necho three")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("script written to %q, want under %q", path, dir)
	}
	if filepath.Ext(path) != ".cmd" {
		t.Errorf("extension = %q, want .cmd", filepath.Ext(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "echo one\r\necho two\r\necho three"
	if string(b) != want {
		t.Errorf("body = %q, want %q", b, want)
	}
}

// A DryRun runner reports success without writing or executing anything.
func TestRunScriptDryRun(t *testing.T) {
	r := &OSRunner{DryRun: true}
	code, err := r.RunScript(context.Background(), "powershell", "Write-Host hi")
	if err != nil || code != 0 {
		t.Errorf("dry-run RunScript = (%d,%v), want (0,nil)", code, err)
	}
}

// An unknown shell is a clear error, not a silent no-op.
func TestRunScriptUnknownShell(t *testing.T) {
	r := &OSRunner{}
	if _, err := r.RunScript(context.Background(), "bash", "echo hi"); err == nil {
		t.Error("expected error for unknown shell")
	}
}

// Copy to an existing-directory destination lands the file INSIDE it (cp
// semantics) instead of failing with "is a directory".
func TestCopyIntoDirectoryDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "shortcut.lnk")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(dir, "StartMenu")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &OSRunner{}
	if err := r.Copy(context.Background(), src, destDir); err != nil {
		t.Fatalf("copy into dir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "shortcut.lnk"))
	if err != nil || string(got) != "data" {
		t.Errorf("expected file copied into dir under its basename: %q err=%v", got, err)
	}
}

func TestExecuteAbortsOnFailureByDefault(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"cmd": 5}}
	list := []swspec.InstallStep{
		{Type: "cmd", ScriptBody: "fail"},
		{Type: "cmd", ScriptBody: "should-not-run"},
	}
	res := Execute(context.Background(), list, rec, HostFacts{})
	if len(res) != 1 {
		t.Fatalf("expected execution to stop after first failure, got %d results", len(res))
	}
	if !res[0].Aborted {
		t.Error("first result should report aborted")
	}
}

func TestContinueOnFailureKeepsGoing(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"cmd": 5}}
	list := []swspec.InstallStep{
		{Type: "cmd", ScriptBody: "fail", ContinueOnFailure: true},
		{Type: "cmd", ScriptBody: "ok"},
	}
	res := Execute(context.Background(), list, rec, HostFacts{})
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
}

func TestSuccessCodesAcceptNonZero(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"msiexec": 3010}}
	list := []swspec.InstallStep{
		{Type: "msi", MSIPath: `C:\pkg\a.msi`, SuccessCodes: []int{0, 3010}},
	}
	res := Execute(context.Background(), list, rec, HostFacts{})
	if len(res) != 1 || res[0].Aborted {
		t.Errorf("3010 (reboot required) should be a success: %+v", res)
	}
}

func TestUnzipStepExtractsToDestination(t *testing.T) {
	// Build a small zip with two entries: a top-level file and a
	// nested file. Confirm both end up where we expect.
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "in.zip")
	zf, err := os.Create(zipPath)
	if err != nil { t.Fatal(err) }
	zw := zip.NewWriter(zf)
	for _, e := range []struct{ name, body string }{
		{"hello.txt", "hi"},
		{"sub/nested.txt", "ok"},
	} {
		w, err := zw.Create(e.name)
		if err != nil { t.Fatal(err) }
		if _, err := w.Write([]byte(e.body)); err != nil { t.Fatal(err) }
	}
	if err := zw.Close(); err != nil { t.Fatal(err) }
	if err := zf.Close(); err != nil { t.Fatal(err) }
	dest := filepath.Join(dir, "out")
	runner := &OSRunner{}
	if err := runner.Unzip(context.Background(), zipPath, dest); err != nil {
		t.Fatalf("unzip: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "hello.txt")); err != nil || string(b) != "hi" {
		t.Errorf("hello.txt: %q / %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "sub", "nested.txt")); err != nil || string(b) != "ok" {
		t.Errorf("sub/nested.txt: %q / %v", b, err)
	}
}

// Zip-slip defence: an entry whose name contains ".." must be
// rejected before any bytes are written, no matter what the resolved
// path would be on disk. The whole extraction aborts so a partial
// state isn't left behind.
func TestUnzipStepRejectsZipSlip(t *testing.T) {
	cases := []string{
		"../escape.txt",
		"sub/../../etc/passwd",
		`..\windows\system32\bad.dll`,
	}
	for _, entry := range cases {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			zipPath := filepath.Join(dir, "in.zip")
			zf, _ := os.Create(zipPath)
			zw := zip.NewWriter(zf)
			w, _ := zw.Create(entry)
			_, _ = w.Write([]byte("pwn"))
			_ = zw.Close()
			_ = zf.Close()
			dest := filepath.Join(dir, "out")
			runner := &OSRunner{}
			err := runner.Unzip(context.Background(), zipPath, dest)
			if err == nil {
				t.Errorf("expected zip-slip rejection for %q, got nil error", entry)
			}
			if !strings.Contains(err.Error(), "unsafe zip entry") {
				t.Errorf("expected 'unsafe zip entry' in error, got %v", err)
			}
		})
	}
}

// Absolute paths in zip entries are likewise unsafe and must be
// rejected -- they'd skip the destination root entirely.
func TestUnzipStepRejectsAbsoluteEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "in.zip")
	zf, _ := os.Create(zipPath)
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("/etc/passwd")
	_, _ = w.Write([]byte("pwn"))
	_ = zw.Close()
	_ = zf.Close()
	dest := filepath.Join(dir, "out")
	runner := &OSRunner{}
	err := runner.Unzip(context.Background(), zipPath, dest)
	if err == nil || !strings.Contains(err.Error(), "unsafe zip entry") {
		t.Errorf("expected unsafe-entry error for absolute path, got %v", err)
	}
}

// Recorder-backed test for the runOne switch: an unzip step records
// the (src, dst) pair on the recorder, the result has no error, and
// the next step still runs.
func TestExecuteUnzipStep(t *testing.T) {
	rec := &Recorder{}
	out := Execute(context.Background(), []swspec.InstallStep{
		{Type: "unzip", SourcePath: `C:\src\bundle.zip`, DestinationPath: `C:\Program Files\App`},
		{Type: "cmd", ScriptBody: "echo done"},
	}, rec, HostFacts{})
	if len(out) != 2 {
		t.Fatalf("want 2 results, got %d", len(out))
	}
	if out[0].Error != nil || out[0].ExitCode != 0 || out[0].Aborted {
		t.Errorf("unzip step: %+v", out[0])
	}
	want := `C:\src\bundle.zip -> C:\Program Files\App`
	if len(rec.Unzips) != 1 || rec.Unzips[0] != want {
		t.Errorf("Unzips = %v, want [%q]", rec.Unzips, want)
	}
}

// The motivating case: a single package carries a Windows 11 zip and a
// Windows 10 zip as two unzip steps, each gated by FilterOS. On a Windows 11
// host only the Windows 11 step extracts; the Windows 10 step is recorded as
// skipped (not run, not aborted) and a following unfiltered step still runs.
func TestExecuteFiltersStepsByOS(t *testing.T) {
	rec := &Recorder{}
	list := []swspec.InstallStep{
		{Type: "unzip", FilterOS: "Windows 11", SourcePath: "win11.zip", DestinationPath: `C:\App`},
		{Type: "unzip", FilterOS: "Windows 10", SourcePath: "win10.zip", DestinationPath: `C:\App`},
		{Type: "cmd", ScriptBody: "echo done"}, // no filter: always runs
	}
	out := Execute(context.Background(), list, rec, HostFacts{OSCaption: "Microsoft Windows 11 Pro"})
	if len(out) != 3 {
		t.Fatalf("want 3 results (one per step), got %d", len(out))
	}
	if out[0].Skipped || out[0].Aborted {
		t.Errorf("Windows 11 step should have run on a Win11 host: %+v", out[0])
	}
	if !out[1].Skipped {
		t.Errorf("Windows 10 step should have been skipped on a Win11 host: %+v", out[1])
	}
	if out[1].Aborted {
		t.Error("a filtered-out step must not abort the package")
	}
	if out[2].Skipped {
		t.Errorf("the unfiltered cmd step should still run: %+v", out[2])
	}
	// Only the Win11 zip should have been extracted.
	if len(rec.Unzips) != 1 || !strings.Contains(rec.Unzips[0], "win11.zip") {
		t.Errorf("expected only win11.zip extracted, got %v", rec.Unzips)
	}
}

// FilterOS matching is a case-insensitive substring of the OS caption, so an
// operator can type "windows 11" and it still matches "Microsoft Windows 11
// Pro". An empty caption (e.g. the OS query failed) matches no filter.
func TestStepAppliesOSMatching(t *testing.T) {
	cases := []struct {
		name    string
		filter  string
		caption string
		want    bool
	}{
		{"no filter always applies", "", "Microsoft Windows 11 Pro", true},
		{"no filter applies even with empty caption", "", "", true},
		{"win11 matches win11 caption", "Windows 11", "Microsoft Windows 11 Pro", true},
		{"win11 does not match win10 caption", "Windows 11", "Microsoft Windows 10 Pro", false},
		{"win10 matches win10 caption", "Windows 10", "Microsoft Windows 10 Pro", true},
		{"case-insensitive", "windows 11", "Microsoft Windows 11 Pro", true},
		{"server edition", "Server", "Microsoft Windows Server 2022 Standard", true},
		{"filter set but caption empty does not match", "Windows 11", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := swspec.InstallStep{Type: "cmd", FilterOS: tc.filter}
			if got := stepApplies(s, HostFacts{OSCaption: tc.caption}); got != tc.want {
				t.Errorf("stepApplies(filter=%q, caption=%q) = %v, want %v",
					tc.filter, tc.caption, got, tc.want)
			}
		})
	}
}

// A filtered-out step that sits before a failing step must not absorb the
// abort: skipping is transparent to the success/failure sequencing.
func TestExecuteSkippedStepDoesNotMaskLaterAbort(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"cmd": 5}}
	list := []swspec.InstallStep{
		{Type: "unzip", FilterOS: "Windows 10", SourcePath: "win10.zip", DestinationPath: `C:\App`},
		{Type: "cmd", ScriptBody: "fail"},
		{Type: "cmd", ScriptBody: "should-not-run"},
	}
	out := Execute(context.Background(), list, rec, HostFacts{OSCaption: "Microsoft Windows 11 Pro"})
	if len(out) != 2 {
		t.Fatalf("want 2 results (skipped + aborted), got %d", len(out))
	}
	if !out[0].Skipped {
		t.Errorf("first step should be skipped: %+v", out[0])
	}
	if !out[1].Aborted {
		t.Errorf("failing cmd step should abort: %+v", out[1])
	}
}

// If Unzip returns an error the result records the error and the
// abort flag fires -- subsequent steps don't run.
func TestExecuteUnzipStepAbortsOnError(t *testing.T) {
	rec := &Recorder{UnzipError: errors.New("disk full")}
	out := Execute(context.Background(), []swspec.InstallStep{
		{Type: "unzip", SourcePath: "a.zip", DestinationPath: "b"},
		{Type: "cmd", ScriptBody: "should not run"},
	}, rec, HostFacts{})
	if len(out) != 1 || !out[0].Aborted || out[0].Error == nil {
		t.Errorf("expected abort with error, got results=%+v", out)
	}
}

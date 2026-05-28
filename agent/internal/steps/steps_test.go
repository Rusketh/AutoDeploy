package steps

import (
	"context"
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
	res := Execute(context.Background(), list, rec)
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
		"cmd /C echo hi",
		"powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command -",
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

func TestExecuteAbortsOnFailureByDefault(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"cmd": 5}}
	list := []swspec.InstallStep{
		{Type: "cmd", ScriptBody: "fail"},
		{Type: "cmd", ScriptBody: "should-not-run"},
	}
	res := Execute(context.Background(), list, rec)
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
	res := Execute(context.Background(), list, rec)
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
}

func TestSuccessCodesAcceptNonZero(t *testing.T) {
	rec := &Recorder{ExitMap: map[string]int{"msiexec": 3010}}
	list := []swspec.InstallStep{
		{Type: "msi", MSIPath: `C:\pkg\a.msi`, SuccessCodes: []int{0, 3010}},
	}
	res := Execute(context.Background(), list, rec)
	if len(res) != 1 || res[0].Aborted {
		t.Errorf("3010 (reboot required) should be a success: %+v", res)
	}
}

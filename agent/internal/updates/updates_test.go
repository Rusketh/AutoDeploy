package updates

import (
	"strings"
	"testing"
)

func TestInstallCommand(t *testing.T) {
	tests := []struct {
		path     string
		wantName string
		wantArgs []string
		wantErr  bool
	}{
		{path: `C:\work\windows10.0-kb5034441-x64.msu`, wantName: "wusa.exe",
			wantArgs: []string{`C:\work\windows10.0-kb5034441-x64.msu`, "/quiet", "/norestart"}},
		{path: `C:\work\UPDATE.MSU`, wantName: "wusa.exe",
			wantArgs: []string{`C:\work\UPDATE.MSU`, "/quiet", "/norestart"}},
		{path: `C:\work\kb5033372.cab`, wantName: "dism.exe",
			wantArgs: []string{"/Online", "/Add-Package", `/PackagePath:C:\work\kb5033372.cab`, "/Quiet", "/NoRestart"}},
		{path: `C:\work\setup.exe`, wantErr: true},
		{path: `C:\work\noextension`, wantErr: true},
	}
	for _, tt := range tests {
		name, args, err := InstallCommand(tt.path)
		if tt.wantErr {
			if err == nil {
				t.Errorf("InstallCommand(%q): expected error, got %s %v", tt.path, name, args)
			}
			continue
		}
		if err != nil {
			t.Errorf("InstallCommand(%q): %v", tt.path, err)
			continue
		}
		if name != tt.wantName {
			t.Errorf("InstallCommand(%q) name = %q, want %q", tt.path, name, tt.wantName)
		}
		if strings.Join(args, " ") != strings.Join(tt.wantArgs, " ") {
			t.Errorf("InstallCommand(%q) args = %v, want %v", tt.path, args, tt.wantArgs)
		}
	}
}

func TestClassifyExitCode(t *testing.T) {
	tests := []struct {
		code       int
		success    bool
		reboot     bool
		reasonPart string
	}{
		{code: 0, success: true},
		{code: 3010, success: true, reboot: true, reasonPart: "reboot required"},
		{code: 1641, success: true, reboot: true},
		{code: 0x00240005, success: true, reboot: true},
		{code: 0x00240006, success: true, reasonPart: "already installed"},
		// 0x80240017 in both unsigned and sign-extended int forms.
		{code: int(uint32(0x80240017)), reasonPart: "not applicable"},
		{code: -2145124329, reasonPart: "not applicable"},
		{code: int(uint32(0x80070422)), reasonPart: "Windows Update service"},
		{code: int(uint32(0x800f081e)), reasonPart: "DISM"},
		{code: 87, reasonPart: "invalid"},
		{code: 1, reasonPart: "exit 1 (0x00000001)"},
	}
	for _, tt := range tests {
		o := ClassifyExitCode(tt.code)
		if o.Success != tt.success || o.RebootRequired != tt.reboot {
			t.Errorf("ClassifyExitCode(%d) = %+v, want success=%v reboot=%v",
				tt.code, o, tt.success, tt.reboot)
		}
		if tt.reasonPart != "" && !strings.Contains(o.Reason, tt.reasonPart) {
			t.Errorf("ClassifyExitCode(%d).Reason = %q, want contains %q",
				tt.code, o.Reason, tt.reasonPart)
		}
	}
}

func TestTailString(t *testing.T) {
	if got := tailString("short", 10); got != "short" {
		t.Errorf("tailString short = %q", got)
	}
	long := strings.Repeat("a", 100) + "END"
	if got := tailString(long, 10); got != "aaaaaaaEND" {
		t.Errorf("tailString long = %q", got)
	}
}

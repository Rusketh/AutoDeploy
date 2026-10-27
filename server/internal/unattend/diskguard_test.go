package unattend

import (
	"encoding/xml"
	"html"
	"strings"
	"testing"
)

// windowsPESetup returns the Microsoft-Windows-Setup component of the
// windowsPE pass, parsed, so tests can assert on structure not substrings.
type peSetup struct {
	DiskConfiguration *struct{} `xml:"DiskConfiguration"`
	ImageInstall      struct {
		OSImage struct {
			InstallTo struct {
				DiskID      int `xml:"DiskID"`
				PartitionID int `xml:"PartitionID"`
			} `xml:"InstallTo"`
			MetaData []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"InstallFrom>MetaData"`
		} `xml:"OSImage"`
	} `xml:"ImageInstall"`
	RunSynchronous []struct {
		Order       int    `xml:"Order"`
		Description string `xml:"Description"`
		Path        string `xml:"Path"`
	} `xml:"RunSynchronous>RunSynchronousCommand"`
}

func parsePESetup(t *testing.T, x []byte) peSetup {
	t.Helper()
	var doc struct {
		Settings []struct {
			Pass       string `xml:"pass,attr"`
			Components []struct {
				Name  string `xml:"name,attr"`
				Inner []byte `xml:",innerxml"`
			} `xml:"component"`
		} `xml:"settings"`
	}
	if err := xml.Unmarshal(x, &doc); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	for _, s := range doc.Settings {
		if s.Pass != "windowsPE" {
			continue
		}
		for _, c := range s.Components {
			if c.Name != "Microsoft-Windows-Setup" {
				continue
			}
			var out peSetup
			if err := xml.Unmarshal([]byte("<c>"+string(c.Inner)+"</c>"), &out); err != nil {
				t.Fatalf("parse Setup component: %v", err)
			}
			return out
		}
	}
	t.Fatal("no windowsPE Microsoft-Windows-Setup component")
	return peSetup{}
}

func TestDiskGuardOmitsStaticDiskConfiguration(t *testing.T) {
	s := Defaults()
	s.WholeDisk = true
	s.DiskGuard = true
	x, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	pe := parsePESetup(t, x)
	// The static wipe is exactly what could hit the USB stick: it must be gone.
	if pe.DiskConfiguration != nil {
		t.Error("guarded export must not carry a static <DiskConfiguration>")
	}
	if strings.Contains(string(x), "WillWipeDisk") {
		t.Error("guarded export must not carry WillWipeDisk")
	}
	// The image still installs to Windows=partition 3 on the guarded disk.
	if got := pe.ImageInstall.OSImage.InstallTo; got.DiskID != 0 || got.PartitionID != 3 {
		t.Errorf("InstallTo = disk %d partition %d, want 0/3", got.DiskID, got.PartitionID)
	}
	if len(pe.RunSynchronous) != 1 {
		t.Fatalf("want exactly the guard RunSynchronous command, got %+v", pe.RunSynchronous)
	}
	g := pe.RunSynchronous[0]
	if g.Order != 1 || !strings.Contains(g.Path, `\AutoDeploy\diskguard.cmd`) {
		t.Errorf("guard command = %+v", g)
	}
	// Setup rejects a RunSynchronousCommand/Path over 259 characters.
	if len(g.Path) > 259 {
		t.Errorf("guard command is %d chars, over the 259 limit", len(g.Path))
	}
	// X: is the WinPE RAM disk, never the install media.
	if strings.Contains(g.Path, " X ") {
		t.Error("guard must not probe X:")
	}
}

func TestDiskGuardFollowsWin11Bypass(t *testing.T) {
	s := Defaults()
	s.TargetOS = "windows-11"
	s.BypassWin11Reqs = true
	s.DiskGuard = true
	x, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	pe := parsePESetup(t, x)
	if len(pe.RunSynchronous) != 6 {
		t.Fatalf("want 5 bypass + 1 guard commands, got %d", len(pe.RunSynchronous))
	}
	for i, c := range pe.RunSynchronous {
		if c.Order != i+1 {
			t.Errorf("command %d has Order %d", i, c.Order)
		}
	}
	if last := pe.RunSynchronous[5]; !strings.Contains(last.Path, "diskguard.cmd") {
		t.Errorf("last command should be the guard, got %q", last.Path)
	}
}

func TestDiskGuardHonoursConfiguredDisk(t *testing.T) {
	s := Defaults()
	s.DiskGuard = true
	s.DiskID = 2
	x, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsePESetup(t, x).ImageInstall.OSImage.InstallTo.DiskID; got != 2 {
		t.Errorf("InstallTo DiskID = %d, want 2", got)
	}
	if !strings.Contains(string(DiskGuardScript(2)), `set "DISK=2"`) {
		t.Error("script must guard the configured disk")
	}
}

func TestDiskGuardOffKeepsStaticLayout(t *testing.T) {
	// Without DiskGuard (every deploy-path answer file) the static
	// DiskConfiguration stays, and no guard command appears.
	x, err := Generate(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	pe := parsePESetup(t, x)
	if pe.DiskConfiguration == nil {
		t.Error("deploy-path answer file lost its DiskConfiguration")
	}
	if strings.Contains(string(x), "diskguard") {
		t.Error("deploy-path answer file must not reference the export disk guard")
	}
}

func TestDiskGuardScript(t *testing.T) {
	sc := string(DiskGuardScript(0))
	if !strings.Contains(sc, "\r\n") || strings.Contains(strings.ReplaceAll(sc, "\r\n", ""), "\n") {
		t.Error("batch file must use CRLF line endings throughout")
	}
	// Order matters: every check must come before the destructive "clean".
	clean := strings.Index(sc, "echo clean")
	for _, check := range []string{
		`if not "%FW%"=="0x2" goto bios`, // UEFI only (GPT layout)
		"if errorlevel 1 goto nodisk",    // disk must exist
		`/C:"USB" /C:"1394"`,             // never a USB/1394 disk
		`goto media`,                     // never the media's disk
	} {
		i := strings.Index(sc, check)
		if i < 0 {
			t.Errorf("script missing check %q", check)
			continue
		}
		if i > clean {
			t.Errorf("check %q comes after the destructive clean", check)
		}
	}
	// The created layout must match what InstallTo PartitionID 3 expects.
	for _, want := range []string{
		"echo convert gpt",
		"echo create partition efi size=384",
		"echo format quick fs=fat32 label=System",
		"echo create partition msr size=16",
		"echo create partition primary",
		"echo format quick fs=ntfs label=Windows",
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("script missing %q", want)
		}
	}
	// A refusal must never fall through to Setup.
	for _, want := range []string{"wpeutil shutdown", ":hang", "goto hang"} {
		if !strings.Contains(sc, want) {
			t.Errorf("refusal path missing %q", want)
		}
	}
	// Re-entrancy: a second copy of the script (media found twice) is a no-op.
	if !strings.Contains(sc, `if exist "%T%\autodeploy-diskguard.done" exit /b 0`) {
		t.Error("script must be idempotent")
	}
	// The "echo select disk 0" lines must use prefix redirection: a trailing
	// "0>" would redirect stdin, not write the diskpart script.
	if strings.Contains(sc, "%DISK%>") || strings.Contains(sc, "%DISK% >") {
		t.Error("diskpart script lines must not end in a digit before '>'")
	}
}

func TestDiskGuardCommandEscapesInXML(t *testing.T) {
	s := Defaults()
	s.DiskGuard = true
	x, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	want := html.EscapeString(diskGuardCommand())
	if !strings.Contains(string(x), want) {
		t.Errorf("answer file does not carry the guard command verbatim")
	}
}

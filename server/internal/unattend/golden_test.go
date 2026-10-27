package unattend

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// goldenCases pins the answer file produced for the NORMAL (PXE / boot-client)
// deploy path across the settings that shape it. The USB/ISO export work only
// adds export-only knobs (DiskGuard), so these outputs must stay byte-for-byte
// what the previous release generated: the golden files under
// testdata/pxe_golden were captured from the generator BEFORE the export
// changes. Regenerate (only when a deploy-path change is intended) with:
//
//	AUTODEPLOY_UPDATE_GOLDEN=1 go test ./internal/unattend -run TestGoldenPXEOutput
func goldenCases() map[string]Settings {
	cases := map[string]Settings{}

	cases["defaults"] = Defaults()

	win11 := Defaults()
	win11.TargetOS = "windows-11"
	win11.BypassWin11Reqs = true
	win11.BypassNRO = true
	cases["win11-bypass"] = win11

	win10 := Defaults()
	win10.TargetOS = "windows-10"
	win10.Edition = "Windows 10 Enterprise"
	win10.BypassWin11Reqs = true // ignored for Win10
	cases["win10"] = win10

	keyed := Defaults()
	keyed.ProductKey = "VK7JG-NPHTM-C97JM-9MPGT-3V66T"
	keyed.DiskID = 1
	cases["productkey-disk1"] = keyed

	nokey := Defaults()
	nokey.SkipProductKey = true
	nokey.Edition = ""
	cases["skip-productkey-default-edition"] = nokey

	cb := Defaults()
	cb.ServerURL = "https://deploy.example.com:8443"
	cb.CallbackUUID = "4C4C4544-0042-3510-8051-B4C04F4E4B32"
	cb.NameTemplate = "LAB-%serial%"
	cb.NameIdentity = NameIdentity{Serial: "ABC123"}
	cb.DomainJoin = &DomainJoin{Domain: "corp.example.com", JoinUser: "svc", JoinPassword: "p&ss<", OU: "OU=Lab,DC=corp"}
	cases["callback-name-domain"] = cb

	whole := Defaults()
	whole.WholeDisk = true
	cases["wholedisk"] = whole

	return cases
}

func TestGoldenPXEOutput(t *testing.T) {
	update := os.Getenv("AUTODEPLOY_UPDATE_GOLDEN") == "1"
	for name, s := range goldenCases() {
		got, err := Generate(s)
		if err != nil {
			t.Fatalf("%s: Generate: %v", name, err)
		}
		path := filepath.Join("testdata", "pxe_golden", name+".xml")
		if update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read golden: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: deploy-path answer file changed (must stay byte-identical)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
}

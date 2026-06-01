package unattend

import (
	"strings"
	"testing"
)

func TestValidateAcceptsGeneratedVariants(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Settings)
	}{
		{"defaults", func(*Settings) {}},
		{"skip key", func(s *Settings) { s.SkipProductKey = true }},
		{"real key", func(s *Settings) { s.ProductKey = "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE" }},
		{"win11 bypass", func(s *Settings) { s.TargetOS = "windows-11"; s.BypassWin11Reqs = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Defaults()
			c.mut(&s)
			x, err := Generate(s) // Generate runs Validate internally
			if err != nil {
				t.Fatalf("Generate/Validate rejected a valid config: %v", err)
			}
			if err := Validate(x); err != nil {
				t.Fatalf("Validate rejected its own output: %v", err)
			}
		})
	}
}

func TestValidateCatchesUserDataOrder(t *testing.T) {
	// AcceptEula before ProductKey -- the bug that aborted Setup at
	// specialize. The validator must reject it.
	bad := []byte(`<?xml version="1.0"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-Setup">
      <UserData>
        <AcceptEula>true</AcceptEula>
        <ProductKey><WillShowUI>Never</WillShowUI></ProductKey>
      </UserData>
    </component>
  </settings>
</unattend>`)
	err := Validate(bad)
	if err == nil {
		t.Fatal("expected an order violation, got nil")
	}
	if !strings.Contains(err.Error(), "ProductKey") || !strings.Contains(err.Error(), "AcceptEula") {
		t.Errorf("error should name the offending elements; got %v", err)
	}
}

func TestValidateCatchesDiskOrderAndMalformed(t *testing.T) {
	// ModifyPartitions before CreatePartitions is wrong.
	badDisk := []byte(`<unattend xmlns="urn:schemas-microsoft-com:unattend"><settings><component><DiskConfiguration><Disk>
	  <ModifyPartitions/><CreatePartitions/>
	</Disk></DiskConfiguration></component></settings></unattend>`)
	if err := Validate(badDisk); err == nil {
		t.Error("expected Disk order violation")
	}
	// Malformed XML is rejected.
	if err := Validate([]byte(`<unattend><settings>`)); err == nil {
		t.Error("expected malformed-XML error")
	}
	// Wrong root.
	if err := Validate([]byte(`<notunattend/>`)); err == nil {
		t.Error("expected wrong-root error")
	}
}

func TestValidateCatchesCredentialsOrder(t *testing.T) {
	// Username before Password in CredentialsType -- the domain-join bug
	// that aborted Win10 Setup at specialize (0x8030000b).
	bad := []byte(`<unattend xmlns="urn:schemas-microsoft-com:unattend"><settings pass="specialize">
  <component name="Microsoft-Windows-UnattendedJoin"><Identification><Credentials>
    <Domain>corp</Domain><Username>u</Username><Password>p</Password>
  </Credentials><JoinDomain>corp</JoinDomain></Identification></component>
</settings></unattend>`)
	err := Validate(bad)
	if err == nil {
		t.Fatal("expected a Credentials order violation, got nil")
	}
	if !strings.Contains(err.Error(), "Username") || !strings.Contains(err.Error(), "Password") {
		t.Errorf("error should name Username/Password; got %v", err)
	}
}

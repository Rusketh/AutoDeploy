package unattend

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestGenerateDefaultsIsValidXML(t *testing.T) {
	x, err := Generate(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	var any struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(x, &any); err != nil {
		t.Fatalf("generated XML failed to parse: %v\n---\n%s", err, x)
	}
	if any.XMLName.Local != "unattend" {
		t.Errorf("root = %q, want unattend", any.XMLName.Local)
	}
}

func TestGenerateIncludesAdminPassword(t *testing.T) {
	s := Defaults()
	s.AdminPassword = "Hunter2-NotReal"
	x, _ := Generate(s)
	if !XMLContainsSecret(x, s.AdminPassword) {
		t.Errorf("admin password missing from XML; unattended setup would fail")
	}
}

func TestGenerateOmitsAdminWhenHidden(t *testing.T) {
	s := Defaults()
	s.HideAdmin = true
	s.AdminPassword = "should-not-appear"
	x, _ := Generate(s)
	if strings.Contains(string(x), "should-not-appear") {
		t.Error("hidden admin: password leaked into XML")
	}
	if strings.Contains(string(x), "<AdministratorPassword>") {
		t.Error("hidden admin: AdministratorPassword block should be absent")
	}
}

func TestGenerateAlwaysIncludesAgentBootstrap(t *testing.T) {
	x, _ := Generate(Defaults())
	if !strings.Contains(string(x), "autodeploy-agent") {
		t.Errorf("expected agent bootstrap in FirstLogonCommands; got\n%s", x)
	}
}

func TestNameStrategies(t *testing.T) {
	cases := []struct {
		strat, name, want string
	}{
		{"random", "ignored", "<ComputerName>*</ComputerName>"},
		{"literal", "LAB-01", "<ComputerName>LAB-01</ComputerName>"},
		{"prefix", "LAB", "<ComputerName>LAB*</ComputerName>"},
	}
	for _, tc := range cases {
		s := Defaults()
		s.NameStrategy = tc.strat
		s.ComputerName = tc.name
		x, _ := Generate(s)
		if !strings.Contains(string(x), tc.want) {
			t.Errorf("strategy %s: missing %q in XML\n%s", tc.strat, tc.want, x)
		}
	}
}

func TestDomainJoinIncluded(t *testing.T) {
	s := Defaults()
	s.DomainJoin = &DomainJoin{
		Domain:       "corp.example",
		OU:           "OU=Lab,DC=corp,DC=example",
		JoinUser:     "joiner@corp.example",
		JoinPassword: "join-password-not-real",
	}
	x, _ := Generate(s)
	for _, w := range []string{
		"<JoinDomain>corp.example</JoinDomain>",
		"<MachineObjectOU>OU=Lab,DC=corp,DC=example</MachineObjectOU>",
		"joiner@corp.example",
		"join-password-not-real",
	} {
		if !strings.Contains(string(x), w) {
			t.Errorf("domain join: missing %q", w)
		}
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	s, err := Parse(`{"computer_name":"LAB-01","name_strategy":"literal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Locale != "en-US" {
		t.Errorf("default locale not applied: %+v", s)
	}
	if s.ComputerName != "LAB-01" {
		t.Errorf("ComputerName lost: %+v", s)
	}
}

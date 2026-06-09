package bitlocker

import (
	"strings"
	"testing"
)

func TestEnhancedPIN(t *testing.T) {
	cases := []struct {
		pin  string
		want bool
	}{
		{"123456", false},
		{"00000000", false},
		{"", false},
		{"  123456  ", false}, // padding trimmed -> still numeric
		{"Bannana10!", true},  // the reported case
		{"abcdef", true},
		{"Passw0rd", true},
		{"1234 5678", true}, // internal space
		{"1234!", true},
		{"pässwört", true}, // non-ASCII letters
	}
	for _, c := range cases {
		if got := enhancedPIN(c.pin); got != c.want {
			t.Errorf("enhancedPIN(%q) = %v, want %v", c.pin, got, c.want)
		}
	}
}

func TestEnhancedPINPolicyScript(t *testing.T) {
	// A plain numeric PIN must not touch system policy.
	if s := enhancedPINPolicyScript("123456"); s != "" {
		t.Errorf("numeric PIN should produce no policy snippet, got %q", s)
	}

	s := enhancedPINPolicyScript("Bannana10!")
	if s == "" {
		t.Fatal("complex PIN should produce a policy snippet")
	}
	for _, want := range []string{
		`HKLM:\SOFTWARE\Policies\Microsoft\FVE`,
		"UseEnhancedPin",
		"DWord",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("policy snippet missing %q:\n%s", want, s)
		}
	}
	// The PIN is a secret and must never be interpolated into the script.
	if strings.Contains(s, "Bannana") {
		t.Errorf("policy snippet must not contain the PIN value:\n%s", s)
	}
}

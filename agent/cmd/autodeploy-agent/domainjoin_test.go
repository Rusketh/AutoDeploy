package main

import "testing"

func TestDomainMatches(t *testing.T) {
	cases := []struct {
		name           string
		current, want  string
		expectedResult bool
	}{
		{"exact fqdn", "corp.example.com", "corp.example.com", true},
		{"case insensitive", "CORP.example.com", "corp.EXAMPLE.com", true},
		// The bug: Windows reports the DNS FQDN, the operator configured
		// the NetBIOS short name. These must be treated as the same domain.
		{"fqdn vs netbios", "corp.example.com", "CORP", true},
		{"netbios vs fqdn", "CORP", "corp.example.com", true},
		{"single label both", "corp", "CORP", true},
		// Genuinely different domains must NOT match.
		{"different domain", "corp.example.com", "other.example.com", false},
		{"different short name", "corp.example.com", "sales", false},
		// Two distinct FQDNs sharing a first label are NOT the same domain.
		{"shared first label diff fqdn", "corp.example.com", "corp.contoso.com", false},
		{"empty current", "", "corp.example.com", false},
		{"empty target", "corp.example.com", "", false},
		{"whitespace trimmed", "  corp.example.com  ", "corp", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := domainMatches(c.current, c.want); got != c.expectedResult {
				t.Fatalf("domainMatches(%q, %q) = %v, want %v",
					c.current, c.want, got, c.expectedResult)
			}
		})
	}
}

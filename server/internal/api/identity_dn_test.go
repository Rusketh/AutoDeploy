package api

import "testing"

func TestOUFromDN(t *testing.T) {
	cases := []struct{ dn, want string }{
		{"CN=LAB-01,OU=Lab,DC=corp,DC=example", "OU=Lab,DC=corp,DC=example"},
		{"CN=PC,OU=A,OU=B,DC=corp,DC=example", "OU=A,OU=B,DC=corp,DC=example"},
		// Escaped comma inside the CN RDN is not a separator.
		{`CN=Smith\, Bob,OU=People,DC=corp`, "OU=People,DC=corp"},
		{"  CN=PC,OU=Lab,DC=corp  ", "OU=Lab,DC=corp"}, // trimmed
		{"", ""},
		{"CN=orphan", ""}, // no parent container
	}
	for _, c := range cases {
		if got := ouFromDN(c.dn); got != c.want {
			t.Errorf("ouFromDN(%q) = %q, want %q", c.dn, got, c.want)
		}
	}
}

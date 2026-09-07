package main

import "testing"

func TestValidComputerName(t *testing.T) {
	valid := []string{"LAB01", "PC-1", "a", "abcdefghijklmno", "WORK-STATION"}
	for _, s := range valid {
		if !validComputerName(s) {
			t.Errorf("validComputerName(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",                 // empty
		"abcdefghijklmnop", // 16 chars, over the NetBIOS limit
		"12345",            // all-numeric is not a valid computer name
		"has space",        // space
		"under_score",      // underscore not allowed in NetBIOS
		"dot.name",         // dot not allowed
		"bad;name",         // metacharacter
	}
	for _, s := range invalid {
		if validComputerName(s) {
			t.Errorf("validComputerName(%q) = true, want false", s)
		}
	}
}

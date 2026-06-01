package unattend

import (
	"strings"
	"testing"
)

func TestExpandName(t *testing.T) {
	id := NameIdentity{Serial: "ABC-123456", UUID: "1a2b3c4d-5e6f-7a8b-9c0d-aabbccddeeff", Agent: "zz-99-aa-bb"}

	// Literal passthrough.
	if got := ExpandName("LAB-01", id); got != "LAB-01" {
		t.Errorf("literal = %q", got)
	}
	// serial(4) = last 4 of sanitized serial (hyphen stripped -> ABC123456).
	if got := ExpandName("PC-%serial(4)%", id); got != "PC-3456" {
		t.Errorf("serial(4) = %q, want PC-3456", got)
	}
	// full serial, hyphen stripped.
	if got := ExpandName("%serial%", id); got != "ABC123456" {
		t.Errorf("serial = %q", got)
	}
	// uuid(6) = last 6 of hyphen-stripped uuid.
	if got := ExpandName("%uuid(6)%", id); got != "ddeeff" {
		t.Errorf("uuid(6) = %q, want ddeeff", got)
	}
	// random(4): 4 chars, from the safe alphabet, prefixed.
	got := ExpandName("LAB-%random(4)%", id)
	if !strings.HasPrefix(got, "LAB-") || len(got) != len("LAB-")+4 {
		t.Errorf("random(4) shape wrong: %q", got)
	}
	for _, r := range got[len("LAB-"):] {
		if !strings.ContainsRune(randomNameChars, r) {
			t.Errorf("random char %q not in safe alphabet", r)
		}
	}
	// random default length is 6.
	if g := ExpandName("%random%", id); len(g) != 6 {
		t.Errorf("default random len = %d, want 6", len(g))
	}
	// Empty template -> empty (caller falls back to "*").
	if g := ExpandName("", id); g != "" {
		t.Errorf("empty template = %q", g)
	}
	// Unknown placeholder drops out (kept literal text around it).
	if g := ExpandName("X-%bogus%-Y", id); g != "X--Y" {
		t.Errorf("unknown placeholder = %q, want X--Y", g)
	}
	// 15-char NetBIOS cap.
	if g := ExpandName("VERYLONGNAME-%serial%", id); len(g) > 15 {
		t.Errorf("not capped to 15: %q (%d)", g, len(g))
	}
	// Illegal chars stripped (a space and a slash in literal text).
	if g := ExpandName("A B/C", id); g != "ABC" {
		t.Errorf("sanitize = %q, want ABC", g)
	}
	// Machine-specific placeholders drop out when identity is empty
	// (preview path) -> just the literal remains.
	if g := ExpandName("PC-%serial%", NameIdentity{}); g != "PC-" {
		t.Errorf("empty-identity serial = %q, want PC-", g)
	}
}

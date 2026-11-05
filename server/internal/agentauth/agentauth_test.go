package agentauth

import "testing"

func TestSameUUID(t *testing.T) {
	same := [][2]string{
		{"4c4c4544-0042-3010-8051-b4c04f4e3132", "4C4C4544-0042-3010-8051-B4C04F4E3132"},
		{"{4c4c4544-0042-3010-8051-b4c04f4e3132}", "4c4c454400423010" + "8051b4c04f4e3132"},
		// Mixed-endian form (first three fields byte-swapped).
		{"4c4c4544-0042-3010-8051-b4c04f4e3132", "44454c4c-4200-1030-8051-b4c04f4e3132"},
		{"not-a-uuid", "NOT-A-UUID"},
	}
	for _, c := range same {
		if !SameUUID(c[0], c[1]) {
			t.Errorf("SameUUID(%q, %q) = false", c[0], c[1])
		}
	}
	diff := [][2]string{
		{"4c4c4544-0042-3010-8051-b4c04f4e3132", "4c4c4544-0042-3010-8051-b4c04f4e3133"},
		{"4c4c4544-0042-3010-8051-b4c04f4e3132", "e2e-clone"},
	}
	for _, c := range diff {
		if SameUUID(c[0], c[1]) {
			t.Errorf("SameUUID(%q, %q) = true", c[0], c[1])
		}
	}
}

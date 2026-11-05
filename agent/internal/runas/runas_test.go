package runas

import (
	"strings"
	"testing"
)

func TestSplitAccount(t *testing.T) {
	cases := map[string][2]string{
		`CORP\alice`:         {"CORP", "alice"},
		`.\admin`:            {".", "admin"},
		`\admin`:             {".", "admin"},
		`admin`:              {".", "admin"},
		`alice@corp.example`: {"", "alice@corp.example"},
		`  CORP\bob  `:       {"CORP", "bob"},
	}
	for in, want := range cases {
		d, u := SplitAccount(in)
		if d != want[0] || u != want[1] {
			t.Errorf("SplitAccount(%q) = %q, %q; want %q, %q", in, d, u, want[0], want[1])
		}
	}
}

func TestExpandWith(t *testing.T) {
	env := []string{`APPDATA=C:\Users\ops\AppData\Roaming`, `TEMP=C:\Users\ops\Temp`, `=C:=C:\`, `Path=C:\Windows`}
	cases := map[string]string{
		`%AppData%\x`:     `C:\Users\ops\AppData\Roaming\x`,
		`%TEMP%;%path%`:   `C:\Users\ops\Temp;C:\Windows`,
		`%pkgdir%\a`:      `%pkgdir%\a`,
		`100%`:            `100%`,
		`50% off %TEMP%`:  `50% off C:\Users\ops\Temp`,
		`%%TEMP%`:         `%C:\Users\ops\Temp`,
		`no vars`:         `no vars`,
		`%UNKNOWN%%TEMP%`: `%UNKNOWN%C:\Users\ops\Temp`,
	}
	for in, want := range cases {
		if got := ExpandWith(env, in); got != want {
			t.Errorf("ExpandWith(%q) = %q, want %q", in, got, want)
		}
	}
	if ExpandWith(nil, "%TEMP%") != "%TEMP%" {
		t.Error("nil env should leave input alone")
	}
}

func TestEnvBlock(t *testing.T) {
	b := envBlock([]string{"b=2", "A=1", "bad\x00", ""})
	// Sorted case-insensitively, NUL separated, double-NUL terminated.
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			sb.WriteByte('|')
		} else {
			sb.WriteRune(rune(c))
		}
	}
	if got := sb.String(); got != "A=1|b=2||" {
		t.Fatalf("envBlock = %q", got)
	}
	if got := envBlock(nil); len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("empty block = %v", got)
	}
	// Non-BMP characters encode as surrogate pairs.
	if u := utf16Encode("😀"); len(u) != 2 || u[0] != 0xD83D || u[1] != 0xDE00 {
		t.Fatalf("surrogates = %x", u)
	}
}

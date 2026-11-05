// Package runas runs a Tool Kit's steps as an account other than the agent's
// own (SYSTEM): a specific domain/local account logged on with its password,
// or the user signed in at the console. It produces a Session that starts
// processes under that account's token (steps.Launcher), expands environment
// variables the way that account sees them, and grants it access to the kit's
// work folder.
//
// Only Windows is supported; elsewhere Logon and LoggedOnUser return
// ErrUnsupported. The helpers in this file are portable so they can be unit
// tested anywhere.
package runas

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned on platforms without Run As support.
var ErrUnsupported = errors.New("running as another account is only supported on Windows")

// ErrNoInteractiveUser is returned by LoggedOnUser when nobody is signed in.
var ErrNoInteractiveUser = errors.New("nobody is signed in to this machine")

// SplitAccount splits an account name into the domain and user name that
// LogonUser expects:
//
//	CORP\alice          -> "CORP", "alice"
//	.\admin  / admin    -> ".",    "admin"     (local account)
//	alice@corp.example  -> "",     "alice@corp.example" (UPN; domain must be empty)
func SplitAccount(account string) (domain, user string) {
	account = strings.TrimSpace(account)
	if i := strings.Index(account, `\`); i >= 0 {
		domain, user = account[:i], account[i+1:]
		if domain == "" {
			domain = "."
		}
		return domain, user
	}
	if strings.Contains(account, "@") {
		return "", account
	}
	return ".", account
}

// ExpandWith expands %NAME% references in s using env ("NAME=value" entries,
// matched case-insensitively, as Windows does). References with no matching
// variable are left untouched so a later expansion (or the program itself)
// can still see them.
func ExpandWith(env []string, s string) string {
	if !strings.Contains(s, "%") || len(env) == 0 {
		return s
	}
	vars := make(map[string]string, len(env))
	for _, kv := range env {
		i := strings.Index(kv, "=")
		if i <= 0 { // skip "=C:=C:\..." style hidden entries and junk
			continue
		}
		vars[strings.ToUpper(kv[:i])] = kv[i+1:]
	}
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "%")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.Index(rest[i+1:], "%")
		if j < 0 {
			b.WriteString(rest)
			break
		}
		name := rest[i+1 : i+1+j]
		if v, ok := vars[strings.ToUpper(name)]; ok && name != "" {
			b.WriteString(rest[:i])
			b.WriteString(v)
			rest = rest[i+1+j+1:]
			continue
		}
		// Not a known variable: keep the first '%' and rescan from the
		// second, which may open a real reference ("100%%TEMP%").
		b.WriteString(rest[:i+1])
		rest = rest[i+1:]
	}
	return b.String()
}

// envBlock encodes env as a Windows environment block: "k=v\0k=v\0\0". The
// block must be sorted case-insensitively for CreateProcess.
func envBlock(env []string) []uint16 {
	sorted := append([]string(nil), env...)
	sortFold(sorted)
	var out []uint16
	for _, kv := range sorted {
		if kv == "" || strings.ContainsRune(kv, 0) {
			continue
		}
		out = append(out, utf16Encode(kv)...)
		out = append(out, 0)
	}
	if len(out) == 0 {
		out = append(out, 0)
	}
	return append(out, 0)
}

func sortFold(s []string) {
	// insertion sort keeps this dependency-free; environments are small.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.ToUpper(s[j]) < strings.ToUpper(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func utf16Encode(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x10000:
			out = append(out, uint16(r))
		default:
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		}
	}
	return out
}

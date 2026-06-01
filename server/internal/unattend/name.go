package unattend

import (
	"crypto/rand"
	"regexp"
	"strconv"
	"strings"
)

// NameIdentity carries the per-machine facts that name-template placeholders
// can reference. Any field may be empty (e.g. the portal "preview" path has
// no machine); placeholders for empty fields expand to nothing.
type NameIdentity struct {
	Serial string // SMBIOS system serial
	UUID   string // SMBIOS system UUID
	Agent  string // AutoDeploy server-minted agent_id
}

// namePlaceholderRe matches %token% or %token(N)%, e.g. %serial%,
// %serial(4)%, %random(6)%. Token is letters only; the optional (N) is a
// length argument.
var namePlaceholderRe = regexp.MustCompile(`%([a-zA-Z]+)(?:\((\d+)\))?%`)

// randomNameChars is the alphabet for %random(N)%: upper-case letters and
// digits, excluding visually ambiguous 0/O/1/I so a name read off a screen
// is unambiguous.
const randomNameChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ExpandName turns a computer-name template into a concrete, valid Windows
// computer name. Supported placeholders (case-insensitive):
//
//	%random(N)%  N random chars (defaults to 6 if N omitted)
//	%serial%     full SMBIOS serial
//	%serial(N)%  last N chars of the serial
//	%uuid%       SMBIOS UUID (hyphens stripped)
//	%uuid(N)%    last N chars of the UUID (hyphens stripped)
//	%agent%      / %agent(N)%  the AutoDeploy agent_id (hyphens stripped)
//
// Literal text is kept as-is. The result is sanitised to characters legal
// in a NetBIOS name (A-Z a-z 0-9 -) and capped at 15 chars. An empty or
// all-empty result yields "" so the caller can fall back to a random name.
func ExpandName(template string, id NameIdentity) string {
	if strings.TrimSpace(template) == "" {
		return ""
	}
	out := namePlaceholderRe.ReplaceAllStringFunc(template, func(m string) string {
		sub := namePlaceholderRe.FindStringSubmatch(m)
		token := strings.ToLower(sub[1])
		n := -1
		if sub[2] != "" {
			if v, err := strconv.Atoi(sub[2]); err == nil {
				n = v
			}
		}
		switch token {
		case "random":
			if n <= 0 {
				n = 6
			}
			return randomString(n)
		case "serial":
			return lastN(stripSep(id.Serial), n)
		case "uuid":
			return lastN(stripSep(id.UUID), n)
		case "agent":
			return lastN(stripSep(id.Agent), n)
		default:
			return "" // unknown placeholder drops out
		}
	})
	name := sanitizeNetBIOS(out)
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

// lastN returns the last n chars of s (whole string if n<=0 or n>=len).
func lastN(s string, n int) string {
	if n <= 0 || n >= len(s) {
		return s
	}
	return s[len(s)-n:]
}

// stripSep removes hyphens/colons/spaces (UUIDs and some serials carry
// them) so a sliced suffix is meaningful.
func stripSep(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ':' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// sanitizeNetBIOS keeps only characters legal in a Windows computer name.
func sanitizeNetBIOS(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// randomString returns n chars from randomNameChars using crypto/rand.
func randomString(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Extremely unlikely; fall back to a fixed filler so we still
		// produce a (less unique) valid name rather than nothing.
		for i := range buf {
			buf[i] = 'X'
		}
		return string(buf)
	}
	out := make([]byte, n)
	for i, v := range buf {
		out[i] = randomNameChars[int(v)%len(randomNameChars)]
	}
	return string(out)
}

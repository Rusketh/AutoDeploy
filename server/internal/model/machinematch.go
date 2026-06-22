package model

import (
	"fmt"
	"regexp"
	"strings"
)

// Empty reports whether the filter has no active criteria. OUSubtree alone is
// not a criterion — it only changes how a set OU is matched. A dynamic group
// must carry a non-empty filter.
func (f MachineFilter) Empty() bool {
	return strings.TrimSpace(f.NameRegex) == "" &&
		strings.TrimSpace(f.OS) == "" &&
		strings.TrimSpace(f.OU) == "" &&
		strings.TrimSpace(f.MemberOf) == ""
}

// compileNameRegex compiles a name pattern case-insensitively, or returns
// (nil, nil) when no pattern is set. A bad pattern is reported as ErrValidation
// so it surfaces as a 400 rather than a 500.
func compileNameRegex(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: name pattern: %v", ErrValidation, err)
	}
	return re, nil
}

// machineDisplayName is the name a NameRegex criterion matches against: the
// agent-reported computer name, falling back to the binding's desired name.
// Same precedence the inventory list and NamesBy* helpers use, so the filter
// matches what the operator sees.
func machineDisplayName(m MachineRecord, b MachineBinding) string {
	if m.ReportedName != "" {
		return m.ReportedName
	}
	return b.MachineName
}

// matchMachineIDs returns the IDs of the machines in recs that satisfy f, given
// bindings and OS captions preloaded and keyed by machine ID. Every set field
// must match (AND). An empty filter matches nothing (a dynamic group is never
// allowed to be empty, so this is a safety default). An invalid NameRegex
// returns an ErrValidation error.
func matchMachineIDs(f MachineFilter, recs []MachineRecord, binds map[ID]MachineBinding, osCaptions map[ID]string) ([]ID, error) {
	if f.Empty() {
		return nil, nil
	}
	re, err := compileNameRegex(f.NameRegex)
	if err != nil {
		return nil, err
	}
	os := strings.ToLower(strings.TrimSpace(f.OS))
	ou := strings.ToLower(strings.TrimSpace(f.OU))
	memberOf := strings.TrimSpace(f.MemberOf)
	out := make([]ID, 0, len(recs))
	for _, m := range recs {
		b := binds[m.ID]
		if re != nil && !re.MatchString(machineDisplayName(m, b)) {
			continue
		}
		if os != "" && !strings.Contains(strings.ToLower(osCaptions[m.ID]), os) {
			continue
		}
		if ou != "" && !ouMatches(ou, b.TargetOU, m.ADDistinguishedName, f.OUSubtree) {
			continue
		}
		if memberOf != "" && !hasGroupMembership(b.GroupMemberships, memberOf) {
			continue
		}
		out = append(out, m.ID)
	}
	return out, nil
}

// ouMatches reports whether a machine's desired OU (targetOU) or observed AD
// distinguished name (adDN) satisfies the already-lower-cased filter ou. In
// subtree mode any candidate that ends with ou matches — the OU itself and any
// child OU or machine DN beneath it. In exact mode the candidate must equal ou,
// which (since adDN is a full machine DN, not an OU) means the machine's desired
// OU is exactly ou.
func ouMatches(ou, targetOU, adDN string, subtree bool) bool {
	for _, c := range []string{strings.ToLower(strings.TrimSpace(targetOU)), strings.ToLower(strings.TrimSpace(adDN))} {
		if c == "" {
			continue
		}
		if subtree {
			if strings.HasSuffix(c, ou) {
				return true
			}
			continue
		}
		if c == ou {
			return true
		}
	}
	return false
}

// hasGroupMembership reports whether want is one of the machine's AD group
// memberships, case-insensitively.
func hasGroupMembership(groups []string, want string) bool {
	for _, g := range groups {
		if strings.EqualFold(strings.TrimSpace(g), want) {
			return true
		}
	}
	return false
}

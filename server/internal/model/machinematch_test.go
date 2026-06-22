package model

import (
	"errors"
	"testing"
)

func TestMachineFilterEmpty(t *testing.T) {
	if !(MachineFilter{}).Empty() {
		t.Error("zero filter should be empty")
	}
	if !(MachineFilter{OUSubtree: true}).Empty() {
		t.Error("OUSubtree alone is not a criterion")
	}
	for _, f := range []MachineFilter{
		{NameRegex: "x"}, {OS: "x"}, {OU: "x"}, {MemberOf: "x"},
	} {
		if f.Empty() {
			t.Errorf("filter %+v should not be empty", f)
		}
	}
}

func sameIDSet(a, b []ID) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[ID]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

func TestMatchMachineIDs(t *testing.T) {
	recs := []MachineRecord{
		{ID: 1, ReportedName: "LAB-PC-01", ADDistinguishedName: "CN=LAB-PC-01,OU=Lab,DC=corp,DC=example"},
		{ID: 2, ReportedName: "LAB-PC-02"},
		{ID: 3, ReportedName: "HR-PC-01"},
	}
	binds := map[ID]MachineBinding{
		1: {MachineName: "LAB-PC-01", TargetOU: "OU=Lab,DC=corp,DC=example", GroupMemberships: []string{"Lab-Admins", "Domain Users"}},
		2: {MachineName: "LAB-PC-02", TargetOU: "OU=Sub,OU=Lab,DC=corp,DC=example", GroupMemberships: []string{"Domain Users"}},
		3: {MachineName: "HR-PC-01", TargetOU: "OU=HR,DC=corp,DC=example"},
	}
	os := map[ID]string{
		1: "Microsoft Windows 11 Pro",
		2: "Microsoft Windows 10 Pro",
		3: "Microsoft Windows 11 Pro",
	}
	must := func(f MachineFilter) []ID {
		ids, err := matchMachineIDs(f, recs, binds, os)
		if err != nil {
			t.Fatalf("match %+v: %v", f, err)
		}
		return ids
	}

	cases := []struct {
		name string
		f    MachineFilter
		want []ID
	}{
		{"name regex case-insensitive", MachineFilter{NameRegex: "^lab-"}, []ID{1, 2}},
		{"os substring", MachineFilter{OS: "windows 11"}, []ID{1, 3}},
		{"ou exact", MachineFilter{OU: "OU=Lab,DC=corp,DC=example"}, []ID{1}},
		{"ou subtree", MachineFilter{OU: "OU=Lab,DC=corp,DC=example", OUSubtree: true}, []ID{1, 2}},
		{"member of", MachineFilter{MemberOf: "lab-admins"}, []ID{1}},
		{"AND of name+os", MachineFilter{NameRegex: "^LAB-", OS: "Windows 11"}, []ID{1}},
		{"empty matches none", MachineFilter{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := must(c.f); !sameIDSet(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	if _, err := matchMachineIDs(MachineFilter{NameRegex: "([a-"}, recs, binds, os); !errors.Is(err, ErrValidation) {
		t.Errorf("invalid regex: want ErrValidation, got %v", err)
	}
}

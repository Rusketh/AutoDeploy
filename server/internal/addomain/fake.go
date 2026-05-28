package addomain

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// FakeDirectory is an in-memory Directory implementation used by tests.
type FakeDirectory struct {
	mu        sync.Mutex
	Computers map[string]string   // DN -> CN
	Groups    map[string][]string // groupName -> list of member DNs
}

// NewFakeDirectory returns an empty fake.
func NewFakeDirectory() *FakeDirectory {
	return &FakeDirectory{
		Computers: map[string]string{},
		Groups:    map[string][]string{},
	}
}

func (f *FakeDirectory) FindComputer(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := "CN=" + ldapEscape(name) + ","
	for dn := range f.Computers {
		if strings.HasPrefix(dn, want) {
			return dn, nil
		}
	}
	return "", nil
}

func (f *FakeDirectory) DeleteComputer(_ context.Context, dn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Computers[dn]; !ok {
		return fmt.Errorf("no such computer: %s", dn)
	}
	delete(f.Computers, dn)
	for g, members := range f.Groups {
		out := members[:0]
		for _, m := range members {
			if m != dn {
				out = append(out, m)
			}
		}
		f.Groups[g] = out
	}
	return nil
}

func (f *FakeDirectory) CreateComputer(_ context.Context, ou, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dn := computerDN(name, ou)
	if _, exists := f.Computers[dn]; exists {
		return "", fmt.Errorf("exists: %s", dn)
	}
	f.Computers[dn] = name
	return dn, nil
}

func (f *FakeDirectory) SetGroupMemberships(_ context.Context, computerDN string, groups []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Remove from all groups first.
	for g, members := range f.Groups {
		out := members[:0]
		for _, m := range members {
			if m != computerDN {
				out = append(out, m)
			}
		}
		f.Groups[g] = out
	}
	// Add to the requested ones.
	for _, g := range groups {
		f.Groups[g] = append(f.Groups[g], computerDN)
	}
	return nil
}

func (f *FakeDirectory) RenameComputer(_ context.Context, dn, newName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Computers[dn]; !ok {
		return "", fmt.Errorf("no such computer: %s", dn)
	}
	// Preserve the containing OU; only rebuild the CN.
	ou := dn
	if i := strings.Index(dn, ","); i >= 0 {
		ou = dn[i+1:]
	}
	newDN := computerDN(newName, ou)
	if _, exists := f.Computers[newDN]; exists {
		return "", fmt.Errorf("exists: %s", newDN)
	}
	delete(f.Computers, dn)
	f.Computers[newDN] = newName
	// Group memberships move with the DN.
	for g, members := range f.Groups {
		for i, m := range members {
			if m == dn {
				members[i] = newDN
			}
		}
		f.Groups[g] = members
	}
	return newDN, nil
}

func (f *FakeDirectory) Close() error { return nil }

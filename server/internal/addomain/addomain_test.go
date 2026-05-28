package addomain

import (
	"context"
	"strings"
	"testing"
)

func TestPrepareComputerDeleteAndReplace(t *testing.T) {
	dir := NewFakeDirectory()
	// Pre-populate an existing object at the target name to force the
	// delete-and-replace path.
	if _, err := dir.CreateComputer(context.Background(),
		"OU=OldLab,DC=corp,DC=example", "LAB-01"); err != nil {
		t.Fatal(err)
	}

	s := &Service{Dir: dir, Enabled: true}
	dn, err := s.PrepareComputer(context.Background(),
		"LAB-01", "OU=NewLab,DC=corp,DC=example",
		[]string{"Lab-Computers", "All-Workstations"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dn, "CN=LAB-01,") || !strings.HasSuffix(dn, "OU=NewLab,DC=corp,DC=example") {
		t.Errorf("DN wrong: %q", dn)
	}
	// Old object should be gone.
	for d := range dir.Computers {
		if strings.Contains(d, "OldLab") {
			t.Errorf("old object survived: %s", d)
		}
	}
	// Groups reconciled.
	if len(dir.Groups["Lab-Computers"]) != 1 || dir.Groups["Lab-Computers"][0] != dn {
		t.Errorf("Lab-Computers membership wrong: %+v", dir.Groups)
	}
	if len(dir.Groups["All-Workstations"]) != 1 {
		t.Errorf("All-Workstations missing: %+v", dir.Groups)
	}
}

func TestPrepareComputerDisabledIsNoOp(t *testing.T) {
	dir := NewFakeDirectory()
	s := &Service{Dir: dir, Enabled: false}
	dn, err := s.PrepareComputer(context.Background(), "LAB-01", "OU=Lab,DC=corp,DC=example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if dn != "" {
		t.Errorf("disabled service should return empty DN, got %q", dn)
	}
	if len(dir.Computers) != 0 {
		t.Errorf("disabled service should not have touched the directory: %+v", dir.Computers)
	}
}

func TestPrepareComputerReconcilesGroups(t *testing.T) {
	dir := NewFakeDirectory()
	// Pre-populate the computer in one group it will no longer be in.
	dn, _ := dir.CreateComputer(context.Background(),
		"OU=Lab,DC=corp,DC=example", "LAB-02")
	dir.Groups["Old-Group"] = []string{dn}

	s := &Service{Dir: dir, Enabled: true}
	if _, err := s.PrepareComputer(context.Background(),
		"LAB-02", "OU=Lab,DC=corp,DC=example",
		[]string{"New-Group"}); err != nil {
		t.Fatal(err)
	}
	if len(dir.Groups["Old-Group"]) != 0 {
		t.Errorf("Old-Group should be empty: %+v", dir.Groups["Old-Group"])
	}
	if len(dir.Groups["New-Group"]) != 1 {
		t.Errorf("New-Group missing: %+v", dir.Groups["New-Group"])
	}
}

func TestPrepareValidates(t *testing.T) {
	s := &Service{Dir: NewFakeDirectory(), Enabled: true}
	if _, err := s.PrepareComputer(context.Background(), "", "OU=x", nil); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := s.PrepareComputer(context.Background(), "n", "", nil); err == nil {
		t.Error("expected error for empty OU")
	}
}

func TestRenameComputer(t *testing.T) {
	dir := NewFakeDirectory()
	_, err := dir.CreateComputer(context.Background(),
		"OU=Lab,DC=corp,DC=example", "OLDNAME")
	if err != nil {
		t.Fatal(err)
	}
	dir.Groups["Lab-Computers"] = []string{"CN=OLDNAME,OU=Lab,DC=corp,DC=example"}
	s := &Service{Dir: dir, Enabled: true}
	newDN, err := s.RenameComputer(context.Background(), "OLDNAME", "NEWNAME")
	if err != nil {
		t.Fatal(err)
	}
	if newDN != "CN=NEWNAME,OU=Lab,DC=corp,DC=example" {
		t.Errorf("newDN = %q", newDN)
	}
	if _, exists := dir.Computers[newDN]; !exists {
		t.Error("new object not present")
	}
	for d := range dir.Computers {
		if strings.Contains(d, "OLDNAME") {
			t.Errorf("old DN survived: %s", d)
		}
	}
	// Group membership followed the rename.
	mem := dir.Groups["Lab-Computers"]
	if len(mem) != 1 || mem[0] != newDN {
		t.Errorf("group membership not updated: %+v", mem)
	}
}

func TestRenameNoSuchObject(t *testing.T) {
	dir := NewFakeDirectory()
	s := &Service{Dir: dir, Enabled: true}
	dn, err := s.RenameComputer(context.Background(), "GHOST", "REAL")
	if err != nil {
		t.Fatalf("rename should be a no-op when object not found, got %v", err)
	}
	if dn != "" {
		t.Errorf("expected empty DN, got %q", dn)
	}
}

func TestRenameDisabledServiceIsNoOp(t *testing.T) {
	s := &Service{Dir: NewFakeDirectory(), Enabled: false}
	dn, err := s.RenameComputer(context.Background(), "X", "Y")
	if err != nil || dn != "" {
		t.Errorf("disabled service should no-op cleanly; got dn=%q err=%v", dn, err)
	}
}

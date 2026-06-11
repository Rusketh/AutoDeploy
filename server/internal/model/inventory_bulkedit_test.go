package model

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestBulkEditBindings(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	images := NewImageRepo(db)

	img, err := images.Create(ctx, Image{Name: "lab"})
	if err != nil {
		t.Fatal(err)
	}

	addMachine := func(uuid string) ID {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO machine_record (system_uuid) VALUES (?)`, uuid); err != nil {
			t.Fatal(err)
		}
		var id ID
		if err := db.QueryRowContext(ctx,
			`SELECT id FROM machine_record WHERE system_uuid=?`, uuid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	m1 := addMachine("uuid-1") // no binding yet
	m2 := addMachine("uuid-2") // existing binding with name + groups
	if err := inv.UpsertBinding(ctx, MachineBinding{
		MachineID: m2, MachineName: "KEEP-ME",
		GroupMemberships: []string{"Old-Group", "Keep-Group"},
	}); err != nil {
		t.Fatal(err)
	}

	// Set OU + image + group changes on both; name untouched.
	updated, err := inv.BulkEditBindings(ctx, []ID{m1, m2, 9999}, BindingEdit{
		ImageID:      &img.ID,
		TargetOU:     strPtr("OU=Lab,DC=corp"),
		AddGroups:    []string{"Lab-Computers", "keep-group"}, // dup of existing, case-insensitive
		RemoveGroups: []string{"old-group"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Errorf("updated = %d, want 2 (unknown id skipped)", updated)
	}

	b1, err := inv.GetBinding(ctx, m1)
	if err != nil {
		t.Fatal(err)
	}
	if b1.ImageID == nil || *b1.ImageID != img.ID || b1.TargetOU != "OU=Lab,DC=corp" {
		t.Errorf("m1 binding = %+v", b1)
	}
	// m1 had no groups: both adds land (it has no existing "Keep-Group"
	// to dedupe against, unlike m2 below).
	if !reflect.DeepEqual(b1.GroupMemberships, []string{"Lab-Computers", "keep-group"}) {
		t.Errorf("m1 groups = %v", b1.GroupMemberships)
	}

	b2, _ := inv.GetBinding(ctx, m2)
	if b2.MachineName != "KEEP-ME" {
		t.Errorf("untouched name changed: %+v", b2)
	}
	if !reflect.DeepEqual(b2.GroupMemberships, []string{"Keep-Group", "Lab-Computers"}) {
		t.Errorf("m2 groups = %v", b2.GroupMemberships)
	}

	// Clear image and name explicitly.
	zero := ID(0)
	if _, err := inv.BulkEditBindings(ctx, []ID{m2}, BindingEdit{
		ImageID:     &zero,
		MachineName: strPtr(""),
	}); err != nil {
		t.Fatal(err)
	}
	b2, _ = inv.GetBinding(ctx, m2)
	if b2.ImageID != nil || b2.MachineName != "" {
		t.Errorf("clear failed: %+v", b2)
	}
	// OU from the earlier edit survives the clear edit.
	if b2.TargetOU != "OU=Lab,DC=corp" {
		t.Errorf("unrelated field changed: %+v", b2)
	}

	// Guard rails.
	if _, err := inv.BulkEditBindings(ctx, []ID{m1}, BindingEdit{}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty edit: %v", err)
	}
	if _, err := inv.BulkEditBindings(ctx, nil, BindingEdit{TargetOU: strPtr("x")}); !errors.Is(err, ErrValidation) {
		t.Errorf("no machines: %v", err)
	}
}

func TestApplyGroupEdits(t *testing.T) {
	tests := []struct {
		name                 string
		current, add, remove []string
		want                 []string
	}{
		{"add to empty", nil, []string{"A"}, nil, []string{"A"}},
		{"dedupe case-insensitive", []string{"Lab"}, []string{"lab", "X"}, nil, []string{"Lab", "X"}},
		{"remove case-insensitive", []string{"Lab", "Old"}, nil, []string{"old"}, []string{"Lab"}},
		{"remove wins over add", []string{"A"}, []string{"B"}, []string{"b"}, []string{"A"}},
		{"blanks dropped", []string{"", "A"}, []string{" ", "B"}, nil, []string{"A", "B"}},
		{"no edits keeps order", []string{"B", "A"}, nil, nil, []string{"B", "A"}},
	}
	for _, tt := range tests {
		if got := applyGroupEdits(tt.current, tt.add, tt.remove); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: applyGroupEdits = %v, want %v", tt.name, got, tt.want)
		}
	}
}

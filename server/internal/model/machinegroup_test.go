package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// seedMachine inserts a machine with a binding (name/OU/AD groups) and an
// optional reported OS caption, returning its ID.
func seedGroupMachine(t *testing.T, inv *InventoryRepo, uuid, name, ou string, adGroups []string, osCaption string) ID {
	t.Helper()
	ctx := context.Background()
	rec, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: uuid})
	if err != nil {
		t.Fatalf("seed %s: %v", uuid, err)
	}
	if err := inv.UpsertBinding(ctx, MachineBinding{
		MachineID: rec.ID, MachineName: name, TargetOU: ou, GroupMemberships: adGroups,
	}); err != nil {
		t.Fatalf("bind %s: %v", uuid, err)
	}
	if osCaption != "" {
		if err := inv.UpdateHardware(ctx, rec.ID, Hardware{OSCaption: osCaption}); err != nil {
			t.Fatalf("hw %s: %v", uuid, err)
		}
	}
	return rec.ID
}

func TestMachineGroupManualMembership(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	repo := NewMachineGroupRepo(db, inv)

	m1 := seedGroupMachine(t, inv, "u1", "PC-1", "", nil, "")
	m2 := seedGroupMachine(t, inv, "u2", "PC-2", "", nil, "")
	_ = seedGroupMachine(t, inv, "u3", "PC-3", "", nil, "")

	g, err := repo.Create(ctx, MachineGroup{Name: "Lab", Kind: MachineGroupManual})
	if err != nil {
		t.Fatal(err)
	}
	if g.Kind != MachineGroupManual {
		t.Fatalf("kind = %q", g.Kind)
	}

	if err := repo.AddMembers(ctx, g.ID, []ID{m1, m2}); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-add.
	if err := repo.AddMembers(ctx, g.ID, []ID{m1}); err != nil {
		t.Fatal(err)
	}
	mem, _ := repo.Members(ctx, g.ID)
	if !sameIDSet(mem, []ID{m1, m2}) {
		t.Errorf("members = %v, want %v", mem, []ID{m1, m2})
	}

	if err := repo.RemoveMembers(ctx, g.ID, []ID{m1}); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.MemberCount(ctx, g.ID); n != 1 {
		t.Errorf("count after remove = %d, want 1", n)
	}

	// Deleting a machine drops it from the group (FK cascade).
	if err := inv.Delete(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.MemberCount(ctx, g.ID); n != 0 {
		t.Errorf("count after machine delete = %d, want 0", n)
	}
}

func TestMachineGroupDynamicResolution(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	repo := NewMachineGroupRepo(db, inv)

	m1 := seedGroupMachine(t, inv, "u1", "LAB-PC-01", "OU=Lab,DC=corp", []string{"Lab-Admins"}, "Microsoft Windows 11 Pro")
	m2 := seedGroupMachine(t, inv, "u2", "LAB-PC-02", "OU=Lab,DC=corp", nil, "Microsoft Windows 10 Pro")
	_ = seedGroupMachine(t, inv, "u3", "HR-PC-01", "OU=HR,DC=corp", nil, "Microsoft Windows 11 Pro")

	g, err := repo.Create(ctx, MachineGroup{
		Name: "Win11 Lab", Kind: MachineGroupDynamic,
		Filter: MachineFilter{NameRegex: "^LAB-", OS: "Windows 11"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := repo.Members(ctx, g.ID)
	if !sameIDSet(mem, []ID{m1}) {
		t.Errorf("dynamic members = %v, want %v", mem, []ID{m1})
	}

	// Membership is live: relax the filter, machine 2 joins without any edit
	// to membership tables.
	g.Filter = MachineFilter{NameRegex: "^LAB-"}
	if err := repo.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	mem, _ = repo.Members(ctx, g.ID)
	if !sameIDSet(mem, []ID{m1, m2}) {
		t.Errorf("relaxed dynamic members = %v, want %v", mem, []ID{m1, m2})
	}

	// A dynamic group rejects manual membership writes.
	if err := repo.AddMembers(ctx, g.ID, []ID{m1}); !errors.Is(err, ErrValidation) {
		t.Errorf("AddMembers to dynamic: want ErrValidation, got %v", err)
	}
}

func TestMachineGroupValidation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewMachineGroupRepo(db, NewInventoryRepo(db))

	if _, err := repo.Create(ctx, MachineGroup{Name: "Empty", Kind: MachineGroupDynamic}); !errors.Is(err, ErrValidation) {
		t.Errorf("dynamic with empty filter: want ErrValidation, got %v", err)
	}
	if _, err := repo.Create(ctx, MachineGroup{Name: "Bad", Kind: "weird"}); !errors.Is(err, ErrValidation) {
		t.Errorf("unknown kind: want ErrValidation, got %v", err)
	}
	if _, err := repo.Create(ctx, MachineGroup{Name: "BadRE", Kind: MachineGroupDynamic, Filter: MachineFilter{NameRegex: "([a-"}}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad regex: want ErrValidation, got %v", err)
	}
	if _, err := repo.Create(ctx, MachineGroup{Name: "Dup", Kind: MachineGroupManual}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, MachineGroup{Name: "Dup", Kind: MachineGroupManual}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: want ErrConflict, got %v", err)
	}
}

func TestMachineGroupNestingAndCycles(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	repo := NewMachineGroupRepo(db, inv)

	m1 := seedGroupMachine(t, inv, "u1", "PC-1", "", nil, "")
	m2 := seedGroupMachine(t, inv, "u2", "PC-2", "", nil, "")

	child, _ := repo.Create(ctx, MachineGroup{Name: "Child", Kind: MachineGroupManual})
	_ = repo.AddMembers(ctx, child.ID, []ID{m2})

	// Parent contains m1 directly and nests Child.
	parent, err := repo.Create(ctx, MachineGroup{
		Name: "Parent", Kind: MachineGroupManual, Children: []ID{child.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.AddMembers(ctx, parent.ID, []ID{m1})

	mem, _ := repo.Members(ctx, parent.ID)
	if !sameIDSet(mem, []ID{m1, m2}) {
		t.Errorf("nested members = %v, want %v (direct + child's)", mem, []ID{m1, m2})
	}

	// Self-nesting is a cycle.
	if err := repo.Update(ctx, MachineGroup{ID: parent.ID, Name: "Parent", Kind: MachineGroupManual, Children: []ID{parent.ID}}); !errors.Is(err, ErrCycle) {
		t.Errorf("self-nest: want ErrCycle, got %v", err)
	}
	// Child trying to nest Parent closes a loop.
	if err := repo.Update(ctx, MachineGroup{ID: child.ID, Name: "Child", Kind: MachineGroupManual, Children: []ID{parent.ID}}); !errors.Is(err, ErrCycle) {
		t.Errorf("loop: want ErrCycle, got %v", err)
	}

	// Deleting the child removes the nesting edge; parent keeps its direct member.
	if err := repo.Delete(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Children) != 0 {
		t.Errorf("parent children after child delete = %v, want none", got.Children)
	}
	mem, _ = repo.Members(ctx, parent.ID)
	if !sameIDSet(mem, []ID{m1}) {
		t.Errorf("parent members after child delete = %v, want %v", mem, []ID{m1})
	}
}

func TestMachineGroupGroupsForMachine(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	repo := NewMachineGroupRepo(db, inv)

	m1 := seedGroupMachine(t, inv, "u1", "LAB-PC", "", nil, "Microsoft Windows 11 Pro")

	manual, _ := repo.Create(ctx, MachineGroup{Name: "Manual", Kind: MachineGroupManual})
	_ = repo.AddMembers(ctx, manual.ID, []ID{m1})
	_, _ = repo.Create(ctx, MachineGroup{Name: "Dyn", Kind: MachineGroupDynamic, Filter: MachineFilter{OS: "Windows 11"}})
	_, _ = repo.Create(ctx, MachineGroup{Name: "Other", Kind: MachineGroupDynamic, Filter: MachineFilter{NameRegex: "^HR-"}})

	gs, err := repo.GroupsForMachine(ctx, m1)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, g := range gs {
		names[g.Name] = true
	}
	if !names["Manual"] || !names["Dyn"] || names["Other"] {
		t.Errorf("GroupsForMachine names = %v, want Manual+Dyn (not Other)", names)
	}
}

func TestBulkPreviewTargetsByGroup(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	groups := NewMachineGroupRepo(db, inv)
	bulk := NewBulkRepo(db, inv)
	bulk.SetGroups(groups)

	m1 := seedGroupMachine(t, inv, "u1", "LAB-PC-01", "", nil, "Microsoft Windows 11 Pro")
	_ = seedGroupMachine(t, inv, "u2", "HR-PC-01", "", nil, "Microsoft Windows 10 Pro")

	g, _ := groups.Create(ctx, MachineGroup{Name: "Win11", Kind: MachineGroupDynamic, Filter: MachineFilter{OS: "Windows 11"}})

	got, err := bulk.PreviewTargets(ctx, BulkTarget{GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != m1 {
		t.Errorf("group-targeted preview = %v, want [m1]", got)
	}

	// A non-existent group resolves to no machines (safe empty target).
	empty, err := bulk.PreviewTargets(ctx, BulkTarget{GroupID: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("unknown group preview = %v, want empty", empty)
	}
}

func TestLogSearchActorsFilter(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	logs := NewLogRepo(db)
	for _, a := range []string{"uuid-a", "uuid-b", "uuid-c"} {
		if err := logs.Append(ctx, LogEvent{Component: "agent", Actor: a, Action: "deploy.ok"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := logs.Search(ctx, LogSearch{Actors: []string{"uuid-a", "uuid-c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Actors IN filter returned %d rows, want 2", len(got))
	}
	for _, e := range got {
		if e.Actor != "uuid-a" && e.Actor != "uuid-c" {
			t.Errorf("unexpected actor %q in results", e.Actor)
		}
	}
}

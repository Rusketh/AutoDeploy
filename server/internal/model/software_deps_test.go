package model

import (
	"context"
	"testing"
)

func TestResolveOrderDependencies(t *testing.T) {
	ctx := context.Background()
	repo := NewSoftwarePackageRepo(openTestDB(t))

	// c (no deps), b -> c, a -> b and c.
	c, _ := repo.Create(ctx, SoftwarePackage{Name: "c"})
	b, _ := repo.Create(ctx, SoftwarePackage{Name: "b", DependsOn: []ID{c.ID}})
	a, _ := repo.Create(ctx, SoftwarePackage{Name: "a", DependsOn: []ID{b.ID, c.ID}})

	// DependsOn round-trips through the DB.
	if got, _ := repo.Get(ctx, a.ID); len(got.DependsOn) != 2 {
		t.Fatalf("a.DependsOn = %v, want 2 entries", got.DependsOn)
	}

	// Resolving a pulls in b and c (auto-include) with deps FIRST and no
	// duplicate c.
	order, warns := repo.ResolveOrder(ctx, []ID{a.ID})
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	pos := map[ID]int{}
	for i, id := range order {
		if _, dup := pos[id]; dup {
			t.Errorf("duplicate package %d in order %v", id, order)
		}
		pos[id] = i
	}
	if len(order) != 3 {
		t.Fatalf("order = %v, want 3 packages", order)
	}
	if !(pos[c.ID] < pos[b.ID] && pos[b.ID] < pos[a.ID]) {
		t.Errorf("order %v must be c < b < a", order)
	}

	// A cycle is broken with a warning rather than looping forever.
	x, _ := repo.Create(ctx, SoftwarePackage{Name: "x"})
	y, _ := repo.Create(ctx, SoftwarePackage{Name: "y", DependsOn: []ID{x.ID}})
	_ = repo.Update(ctx, SoftwarePackage{ID: x.ID, Name: "x", DependsOn: []ID{y.ID}})
	cyc, cwarns := repo.ResolveOrder(ctx, []ID{x.ID})
	if len(cyc) != 2 || len(cwarns) == 0 {
		t.Errorf("cycle resolve = %v warns=%v, want both packages + a warning", cyc, cwarns)
	}

	// A missing dependency warns but doesn't drop the dependent.
	z, _ := repo.Create(ctx, SoftwarePackage{Name: "z", DependsOn: []ID{99999}})
	zorder, zwarns := repo.ResolveOrder(ctx, []ID{z.ID})
	if len(zorder) != 1 || zorder[0] != z.ID || len(zwarns) == 0 {
		t.Errorf("missing-dep resolve = %v warns=%v, want [z] + warning", zorder, zwarns)
	}
}

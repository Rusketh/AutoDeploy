package api

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newDesiredNameRepos(t *testing.T) (Repos, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	return Repos{
		Images:    model.NewImageRepo(db),
		Unattend:  model.NewUnattendRepo(db),
		ISOs:      model.NewISORepo(db),
		Inventory: inv,
		Resolver:  resolve.New(model.NewImageRepo(db), model.NewISORepo(db), model.NewUnattendRepo(db)),
	}, ctx
}

func TestResolveDesiredName_LiteralBinding(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1", SystemSerial: "SER123"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "LAB01"}); err != nil {
		t.Fatal(err)
	}
	// Machine still carries a random installed name -> reconcile to the binding.
	m.ReportedName = "DESKTOP-RANDOM"
	if got := resolveDesiredName(ctx, r, m); got != "LAB01" {
		t.Errorf("desired name = %q, want LAB01", got)
	}
	// Once the machine already reports the intended name, no rename is asked.
	m.ReportedName = "lab01" // case-insensitive match
	if got := resolveDesiredName(ctx, r, m); got != "" {
		t.Errorf("desired name = %q, want empty (already named)", got)
	}
}

func TestResolveDesiredName_RandomTemplateIsSkipped(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u2", SystemSerial: "SER999"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "PC-%random(4)%"}); err != nil {
		t.Fatal(err)
	}
	m.ReportedName = "DESKTOP-XYZ"
	// A %random% name is non-deterministic: reconciling it would rename forever.
	if got := resolveDesiredName(ctx, r, m); got != "" {
		t.Errorf("desired name = %q, want empty for a random template", got)
	}
}

func TestResolveDesiredName_ImageTemplate(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	ua, err := r.Unattend.Create(ctx, model.Unattend{
		Name:         "t",
		SettingsJSON: `{"name_template":"LAB-%serial(4)%"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := r.Images.Create(ctx, model.Image{Name: "base", UnattendID: &ua.ID})
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u3", SystemSerial: "ABC123DEF"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	m.ReportedName = "DESKTOP-INIT"
	got := resolveDesiredName(ctx, r, m)
	// Deterministic (hardware-derived): non-empty, stable, and derived from serial.
	if got == "" {
		t.Fatal("expected a deterministic name from the image template")
	}
	if again := resolveDesiredName(ctx, r, m); again != got {
		t.Errorf("name not deterministic: %q then %q", got, again)
	}
}

func TestResolveDesiredName_NoBinding(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u4"})
	if err != nil {
		t.Fatal(err)
	}
	m.ReportedName = "DESKTOP-KEEP"
	// No binding and no name source -> keep the current (random) name.
	if got := resolveDesiredName(ctx, r, m); got != "" {
		t.Errorf("desired name = %q, want empty with no binding", got)
	}
}

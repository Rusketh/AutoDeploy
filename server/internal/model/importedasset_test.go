package model

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseCSV(t *testing.T) {
	// Header row in a non-default order, with a common alias and blank lines.
	in := `name,serial,ou,model
PC-01,ABC123,OU=Sales,Latitude 5520

PC-02, DEF456 ,OU=Eng,OptiPlex 7090
`
	assets, skipped, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %v", skipped)
	}
	if len(assets) != 2 {
		t.Fatalf("want 2 assets, got %d: %+v", len(assets), assets)
	}
	if assets[0].Serial != "ABC123" || assets[0].Model != "Latitude 5520" ||
		assets[0].Name != "PC-01" || assets[0].OU != "OU=Sales" {
		t.Errorf("row 0 wrong: %+v", assets[0])
	}
	// Trimming of surrounding spaces.
	if assets[1].Serial != "DEF456" {
		t.Errorf("serial not trimmed: %q", assets[1].Serial)
	}
}

func TestParseCSVNoHeaderFixedOrder(t *testing.T) {
	in := "SER1,ModelX,Host1,OU=A\nSER2,ModelY,,\n"
	assets, skipped, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 || len(assets) != 2 {
		t.Fatalf("want 2 assets no skips, got %d assets %v skips", len(assets), skipped)
	}
	if assets[0].Name != "Host1" || assets[1].Name != "" {
		t.Errorf("fixed-order parse wrong: %+v", assets)
	}
}

func TestParseCSVSkipsRowsMissingKey(t *testing.T) {
	in := "serial,model,name,ou\n,NoSerial,PC,OU=X\nSER9,,PC,OU=X\nSER10,ModelZ,PC,OU=X\n"
	assets, skipped, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Serial != "SER10" {
		t.Errorf("want only SER10, got %+v", assets)
	}
	if len(skipped) != 2 {
		t.Errorf("want 2 skip messages, got %v", skipped)
	}
}

func TestImportedAssetUpsertDedupe(t *testing.T) {
	ctx := context.Background()
	repo := NewImportedAssetRepo(openTestDB(t))

	ins, upd, err := repo.Upsert(ctx, []ImportedAsset{
		{Serial: "S1", Model: "M1", Name: "old", OU: "OU=old"},
		{Serial: "S2", Model: "M2", Name: "two"},
	})
	if err != nil || ins != 2 || upd != 0 {
		t.Fatalf("first upsert: ins=%d upd=%d err=%v", ins, upd, err)
	}

	// Re-importing S1/M1 updates in place; S3 is new.
	ins, upd, err = repo.Upsert(ctx, []ImportedAsset{
		{Serial: "S1", Model: "M1", Name: "new", OU: "OU=new", Overwrite: true},
		{Serial: "S3", Model: "M3", Name: "three"},
	})
	if err != nil || ins != 1 || upd != 1 {
		t.Fatalf("second upsert: ins=%d upd=%d err=%v", ins, upd, err)
	}

	all, _ := repo.List(ctx)
	if len(all) != 3 {
		t.Fatalf("want 3 rows, got %d", len(all))
	}
	got, err := repo.MatchByIdentity(ctx, "S1", "M1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" || got.OU != "OU=new" || !got.Overwrite {
		t.Errorf("upsert did not replace fields: %+v", got)
	}
}

func TestMatchByIdentity(t *testing.T) {
	ctx := context.Background()
	repo := NewImportedAssetRepo(openTestDB(t))
	if _, _, err := repo.Upsert(ctx, []ImportedAsset{{Serial: "abc", Model: "Latitude 5520", Name: "PC"}}); err != nil {
		t.Fatal(err)
	}

	// Case-insensitive on both serial and model.
	if _, err := repo.MatchByIdentity(ctx, "ABC", "latitude 5520"); err != nil {
		t.Errorf("case-insensitive match failed: %v", err)
	}
	// Serial matches but model differs -> no match (composite key).
	if _, err := repo.MatchByIdentity(ctx, "abc", "OptiPlex"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound on model mismatch, got %v", err)
	}
	// Empty serial or model never matches.
	if _, err := repo.MatchByIdentity(ctx, "", "Latitude 5520"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound on empty serial, got %v", err)
	}
}

func TestMatchByIdentitySkipsMatched(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewImportedAssetRepo(db)
	inv := NewInventoryRepo(db)
	if _, _, err := repo.Upsert(ctx, []ImportedAsset{{Serial: "S1", Model: "M1", Name: "PC"}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.MatchByIdentity(ctx, "S1", "M1")
	if err != nil {
		t.Fatal(err)
	}
	// Create a machine to reference, then mark matched.
	m, err := inv.UpsertFromIdentity(ctx, testIdentity("uuid-1", "S1", "M1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkMatched(ctx, got.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	// Already matched -> no longer returned.
	if _, err := repo.MatchByIdentity(ctx, "S1", "M1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("matched row should not match again, got %v", err)
	}
}

func TestWipeAll(t *testing.T) {
	ctx := context.Background()
	repo := NewImportedAssetRepo(openTestDB(t))
	_, _, _ = repo.Upsert(ctx, []ImportedAsset{
		{Serial: "S1", Model: "M1"}, {Serial: "S2", Model: "M2"},
	})
	n, err := repo.WipeAll(ctx)
	if err != nil || n != 2 {
		t.Fatalf("WipeAll = %d, %v; want 2", n, err)
	}
	if c, _ := repo.Count(ctx); c != 0 {
		t.Errorf("count after wipe = %d, want 0", c)
	}
}

func TestSweepMatched(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewImportedAssetRepo(db)
	inv := NewInventoryRepo(db)

	// S1/M1: will be marked matched. S2/M2: "known" (machine exists but no
	// import apply). S3/M3: neither -> survives the sweep.
	if _, _, err := repo.Upsert(ctx, []ImportedAsset{
		{Serial: "S1", Model: "M1"},
		{Serial: "S2", Model: "M2"},
		{Serial: "S3", Model: "M3"},
	}); err != nil {
		t.Fatal(err)
	}

	// Machine for S1/M1 (do NOT wire the importer, so no auto-apply/mark),
	// then manually mark the row matched.
	m1, _ := inv.UpsertFromIdentity(ctx, testIdentity("u1", "S1", "M1"))
	got, _ := repo.MatchByIdentity(ctx, "S1", "M1")
	_ = repo.MarkMatched(ctx, got.ID, m1.ID)
	// Machine for S2/M2 exists but its import row is untouched -> "known".
	_, _ = inv.UpsertFromIdentity(ctx, testIdentity("u2", "S2", "M2"))

	n, err := repo.SweepMatched(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("sweep removed %d, want 2", n)
	}
	remaining, _ := repo.List(ctx)
	if len(remaining) != 1 || remaining[0].Serial != "S3" {
		t.Errorf("want only S3 left, got %+v", remaining)
	}
}

package model

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestDomainJoinRepo(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	images := NewImageRepo(db)
	dj := NewDomainJoinRepo(db, bx)

	img, err := images.Create(ctx, Image{Name: "win11-domain"})
	if err != nil {
		t.Fatal(err)
	}

	// No config yet.
	if _, err := dj.Get(ctx, img.ID); err == nil {
		t.Error("expected ErrNotFound before any config is set")
	}

	// Set with a password.
	cfg := ImageDomainJoin{
		ImageID: img.ID, Enabled: true,
		Domain: "corp.example.com", OU: "OU=Workstations,DC=corp,DC=example",
		JoinUser: "CORP\\join",
	}
	if err := dj.Set(ctx, cfg, "s3cret-join-pw"); err != nil {
		t.Fatal(err)
	}
	got, err := dj.Get(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Domain != "corp.example.com" || got.JoinUser != "CORP\\join" {
		t.Errorf("config round-trip wrong: %+v", got)
	}
	if !got.HasPassword {
		t.Error("HasPassword should be true")
	}
	if pw, _ := dj.RetrievePassword(ctx, img.ID); pw != "s3cret-join-pw" {
		t.Errorf("password decrypt = %q", pw)
	}
	// The plaintext password must not be stored in the row.
	var cipher string
	_ = db.QueryRowContext(ctx,
		`SELECT join_password_cipher FROM image_domain_join WHERE image_id=?`, img.ID).Scan(&cipher)
	if cipher == "" || cipher == "s3cret-join-pw" {
		t.Errorf("password stored in cleartext or missing: %q", cipher)
	}

	// Empty password on update PRESERVES the stored secret; other fields update.
	cfg.Domain = "corp2.example.com"
	if err := dj.Set(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = dj.Get(ctx, img.ID)
	if got.Domain != "corp2.example.com" {
		t.Errorf("domain not updated: %q", got.Domain)
	}
	if pw, _ := dj.RetrievePassword(ctx, img.ID); pw != "s3cret-join-pw" {
		t.Errorf("blank password should preserve the secret; got %q", pw)
	}

	// A new non-empty password replaces it.
	if err := dj.Set(ctx, cfg, "rotated-pw"); err != nil {
		t.Fatal(err)
	}
	if pw, _ := dj.RetrievePassword(ctx, img.ID); pw != "rotated-pw" {
		t.Errorf("password not rotated; got %q", pw)
	}
}

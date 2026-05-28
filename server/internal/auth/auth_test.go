package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestCreateAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, err := r.CreateUser(ctx, "alice", "hunter2-correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Username != "alice" {
		t.Errorf("created wrong: %+v", u)
	}
	got, err := r.Authenticate(ctx, "alice", "hunter2-correct-horse")
	if err != nil || got.ID != u.ID {
		t.Errorf("auth = %+v err=%v", got, err)
	}
	if _, err := r.Authenticate(ctx, "alice", "wrong"); err == nil {
		t.Error("expected error on wrong password")
	}
	if _, err := r.Authenticate(ctx, "bob", "anything"); err == nil {
		t.Error("expected error for unknown user")
	}
}

func TestDisabledAccountCannotAuthenticate(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw1")
	if err := r.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authenticate(ctx, "alice", "pw1"); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected disabled error, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw")
	tok, err := r.CreateSession(ctx, u.ID, time.Hour)
	if err != nil || tok == "" {
		t.Fatalf("session: %v", err)
	}
	uid, err := r.LookupSession(ctx, tok)
	if err != nil || uid != u.ID {
		t.Errorf("lookup = %d err=%v want %d", uid, err, u.ID)
	}
	if err := r.DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LookupSession(ctx, tok); err == nil {
		t.Error("expected error after delete")
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw")
	tok, _ := r.CreateSession(ctx, u.ID, -time.Hour) // already expired
	if _, err := r.LookupSession(ctx, tok); err == nil {
		t.Error("expected expired session to be rejected")
	}
}

func TestAccessPINFlow(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	s := MustNewSettingsRepo(r)

	if on, _ := s.AccessPINEnabled(ctx); on {
		t.Error("PIN should be off by default")
	}
	if err := s.SetAccessPIN(ctx, "1234"); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.AccessPINEnabled(ctx); !on {
		t.Error("PIN should be on after set")
	}
	ok, _ := s.ValidateAccessPIN(ctx, "1234")
	if !ok {
		t.Error("correct PIN should match")
	}
	ok, _ = s.ValidateAccessPIN(ctx, "wrong")
	if ok {
		t.Error("wrong PIN should not match")
	}
	if err := s.SetAccessPIN(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.AccessPINEnabled(ctx); on {
		t.Error("PIN should be off after empty set")
	}
}

func TestPINLockout(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	s := MustNewSettingsRepo(r)
	_ = s.SetAccessPIN(ctx, "1234")

	locked, _ := s.PINLockedOut(ctx, "u1")
	if locked {
		t.Error("should not be locked initially")
	}
	for i := 0; i < maxAttemptsPerWindow; i++ {
		_ = s.RecordPINAttempt(ctx, "u1", false)
	}
	locked, _ = s.PINLockedOut(ctx, "u1")
	if !locked {
		t.Error("should be locked after enough failures")
	}
	// Other machines unaffected.
	if locked2, _ := s.PINLockedOut(ctx, "u2"); locked2 {
		t.Error("u2 should not be locked")
	}
}

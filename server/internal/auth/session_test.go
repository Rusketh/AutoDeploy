package auth

import (
	"context"
	"testing"
	"time"
)

// setSession rewrites a session's timestamps, standing in for time passing.
func setSession(t *testing.T, r *Repo, tok string, created, expires time.Time) {
	t.Helper()
	if _, err := r.DB.ExecContext(context.Background(),
		`UPDATE user_session SET created_at=?, expires_at=? WHERE token=?`, created, expires, tok); err != nil {
		t.Fatal(err)
	}
}

func storedExpiry(t *testing.T, r *Repo, tok string) time.Time {
	t.Helper()
	var exp time.Time
	if err := r.DB.QueryRowContext(context.Background(),
		`SELECT expires_at FROM user_session WHERE token=?`, tok).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestTouchSessionSlidesIdleExpiry(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw")
	tok, _ := r.CreateSession(ctx, u.ID, SessionIdleTimeout)

	// Ten idle hours later (2h left) a request slides it back to a full
	// idle window.
	now := time.Now()
	setSession(t, r, tok, now.Add(-10*time.Hour), now.Add(2*time.Hour))
	info, err := r.TouchSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Extended || info.UserID != u.ID {
		t.Fatalf("want an extended session for alice, got %+v", info)
	}
	want := now.Add(SessionIdleTimeout)
	if got := storedExpiry(t, r, tok); got.Before(want.Add(-5*time.Second)) || got.After(want.Add(5*time.Second)) {
		t.Errorf("expires_at = %v, want ~%v", got, want)
	}

	// An immediate second request is throttled: no write.
	info, err = r.TouchSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if info.Extended {
		t.Errorf("second touch within a minute should not write, got %+v", info)
	}
}

func TestTouchSessionCappedAtMaxLifetime(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw")
	tok, _ := r.CreateSession(ctx, u.ID, SessionIdleTimeout)

	// Signed in almost 7 days ago: sliding may not pass the deadline.
	now := time.Now()
	created := now.Add(-SessionMaxLifetime + time.Hour)
	setSession(t, r, tok, created, now.Add(10*time.Minute))
	info, err := r.TouchSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	dl := SessionDeadline(created)
	if d := info.ExpiresAt.Sub(dl); d > time.Second || d < -time.Second {
		t.Errorf("expiry %v should be capped at the deadline %v", info.ExpiresAt, dl)
	}

	// Past the deadline the session is gone, however active it was.
	setSession(t, r, tok, now.Add(-SessionMaxLifetime-time.Hour), now.Add(-time.Minute))
	if _, err := r.TouchSession(ctx, tok); err == nil {
		t.Error("expected a session past its deadline to be rejected")
	}
	if _, err := r.LookupSession(ctx, tok); err == nil {
		t.Error("expired session should have been deleted")
	}
}

// A session created by the previous (fixed 12h) server keeps working after
// the upgrade: its stored expiry is honoured and it starts sliding.
func TestTouchSessionKeepsPreUpgradeSessions(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	u, _ := r.CreateUser(ctx, "alice", "pw")
	tok, _ := r.CreateSession(ctx, u.ID, 12*time.Hour) // old fixed TTL
	if _, err := r.LookupSession(ctx, tok); err != nil {
		t.Fatalf("old lookup must still work: %v", err)
	}
	info, err := r.TouchSession(ctx, tok)
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if info.CreatedAt.IsZero() {
		t.Error("created_at (DEFAULT CURRENT_TIMESTAMP) should be read back")
	}
	st, err := r.SessionStatus(ctx, tok)
	if err != nil || st.UserID != u.ID || st.Extended {
		t.Errorf("status = %+v err=%v", st, err)
	}
}

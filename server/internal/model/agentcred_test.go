package model

import (
	"path/filepath"

	"context"
	"errors"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"testing"
	"time"
)

func TestAgentCredentialIssueVerifyRotate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	creds := NewAgentCredentialRepo(db)
	m := seedMachine(t, inv, "cred-1", "PC1", "")

	if c, _ := creds.Get(ctx, m); c.Status != CredLegacy {
		t.Fatalf("new machine status = %s", c.Status)
	}
	s1, err := creds.Issue(ctx, m, "first-contact", false)
	if err != nil || len(s1) < MinSecretLength {
		t.Fatalf("issue: %q %v", s1, err)
	}
	if ok, _ := creds.Verify(ctx, m, s1); !ok {
		t.Fatal("issued secret rejected")
	}
	if ok, _ := creds.Verify(ctx, m, "wrong"); ok {
		t.Fatal("wrong secret accepted")
	}

	// Rotation: s1 -> s2. Both work until s2 is used; then only s2.
	s2, _ := NewAgentSecret()
	if err := creds.Rotate(ctx, m, s1, s2); err != nil {
		t.Fatal(err)
	}
	if c, _ := creds.Get(ctx, m); !c.RotationUnconfirmed {
		t.Fatal("rotation should be unconfirmed")
	}
	if ok, _ := creds.Verify(ctx, m, s1); !ok {
		t.Fatal("previous secret must stay valid until the new one is used")
	}
	// A retried rotation (response lost) is idempotent, from either secret.
	if err := creds.Rotate(ctx, m, s1, s2); err != nil {
		t.Fatalf("retry with old secret: %v", err)
	}
	if err := creds.Rotate(ctx, m, s2, s2); err != nil {
		t.Fatalf("retry with new secret: %v", err)
	}
	if ok, _ := creds.Verify(ctx, m, s2); !ok {
		t.Fatal("new secret rejected")
	}
	if ok, _ := creds.Verify(ctx, m, s1); ok {
		t.Fatal("old secret still valid after the new one was used")
	}
	if c, _ := creds.Get(ctx, m); c.RotationUnconfirmed {
		t.Fatal("rotation not confirmed by use")
	}

	// Rotation needs a valid secret and a strong new one.
	s3, _ := NewAgentSecret()
	if err := creds.Rotate(ctx, m, "bogus", s3); !errors.Is(err, ErrBadSecret) {
		t.Fatalf("bad presented secret: %v", err)
	}
	if err := creds.Rotate(ctx, m, s2, "short"); !errors.Is(err, ErrValidation) {
		t.Fatalf("weak new secret: %v", err)
	}

	// An agent that lost its pending secret (s3 never saved) and retries
	// from its committed secret with another candidate still converges.
	if err := creds.Rotate(ctx, m, s2, s3); err != nil {
		t.Fatal(err)
	}
	s4, _ := NewAgentSecret()
	if err := creds.Rotate(ctx, m, s2, s4); err != nil {
		t.Fatalf("rotation from the still-valid previous secret: %v", err)
	}
	if ok, _ := creds.Verify(ctx, m, s4); !ok {
		t.Fatal("s4 rejected")
	}
}

func TestAgentCredentialDeployKeepsPrevious(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	creds := NewAgentCredentialRepo(db)
	m := seedMachine(t, NewInventoryRepo(db), "cred-2", "PC2", "")
	old, _ := creds.Issue(ctx, m, "first-contact", false)
	deploy, _ := creds.Issue(ctx, m, "pxe", true)
	if ok, _ := creds.Verify(ctx, m, old); !ok {
		t.Fatal("an aborted deploy must not lock out the existing OS")
	}
	if ok, _ := creds.Verify(ctx, m, deploy); !ok {
		t.Fatal("deploy secret rejected")
	}
	if ok, _ := creds.Verify(ctx, m, old); ok {
		t.Fatal("old secret valid after the new OS authenticated")
	}

	// Revoke: nothing works, and the machine needs approval.
	if err := creds.Revoke(ctx, m); err != nil {
		t.Fatal(err)
	}
	if ok, _ := creds.Verify(ctx, m, deploy); ok {
		t.Fatal("revoked secret accepted")
	}
	if c, _ := creds.Get(ctx, m); c.Status != CredApproval {
		t.Fatalf("status after revoke = %s", c.Status)
	}
}

func TestAgentCredentialRequestsGrantsTokens(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	creds := NewAgentCredentialRepo(db)
	m := seedMachine(t, inv, "cred-3", "PC3", "")

	r1, created, err := creds.RequestCredential(ctx, m, "nonce-a-0123456789012345678901234567", "conflict", "10.0.0.5")
	if err != nil || !created || r1.Status != CredRequestPending {
		t.Fatalf("request: %+v %v", r1, err)
	}
	again, againCreated, _ := creds.RequestCredential(ctx, m, "nonce-a-0123456789012345678901234567", "conflict", "10.0.0.5")
	if again.ID != r1.ID || againCreated {
		t.Fatal("same nonce created a second request")
	}
	r2, _, _ := creds.RequestCredential(ctx, m, "nonce-b-0123456789012345678901234567", "", "10.0.0.9")
	if n, _ := creds.PendingRequestCount(ctx); n != 2 {
		t.Fatalf("pending = %d", n)
	}
	got, err := creds.Decide(ctx, r1.ID, true, "admin")
	if err != nil || got.Status != CredRequestApproved || got.DecidedBy != "admin" {
		t.Fatalf("approve: %+v %v", got, err)
	}
	if other, _, _ := creds.RequestCredential(ctx, m, "nonce-b-0123456789012345678901234567", "", ""); other.ID != r2.ID || other.Status != CredRequestDenied {
		t.Fatalf("competing request not denied: %+v", other)
	}
	if err := creds.MarkRedeemed(ctx, r1.ID); err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkRedeemed(ctx, r1.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("redeemed twice")
	}

	// Deploy grants are one-time and expire.
	if creds.RedeemDeployGrant(ctx, m) {
		t.Fatal("grant without granting")
	}
	_ = creds.GrantDeploy(ctx, m, time.Hour)
	if !creds.RedeemDeployGrant(ctx, m) || creds.RedeemDeployGrant(ctx, m) {
		t.Fatal("grant not one-time")
	}
	_ = creds.GrantDeploy(ctx, m, -time.Minute)
	if creds.RedeemDeployGrant(ctx, m) {
		t.Fatal("expired grant redeemed")
	}

	// PIN deploy authorisation window.
	if creds.DeployAuthorized(ctx, "cred-3", time.Minute) {
		t.Fatal("authorised without the PIN")
	}
	_ = creds.AuthorizeDeploy(ctx, "cred-3")
	if !creds.DeployAuthorized(ctx, "CRED-3", time.Minute) {
		t.Fatal("PIN authorisation not recorded")
	}

	// Media tokens: no expiry; with a secrets box every export reuses the
	// same token; revoking or replacing invalidates older media.
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	creds.WithSecretsBox(bx)
	img, _ := NewImageRepo(db).Create(ctx, Image{Name: "img"})
	if creds.ImageEnrollTokenInfo(ctx, img.ID).Exists {
		t.Fatal("token before any export")
	}
	t1, _ := creds.EnsureImageEnrollToken(ctx, img.ID)
	t1again, _ := creds.EnsureImageEnrollToken(ctx, img.ID)
	if t1 == "" || t1 != t1again || !creds.ValidImageEnrollToken(ctx, img.ID, t1) || creds.ValidImageEnrollToken(ctx, img.ID, "nope") {
		t.Fatal("re-export must reuse the standing token")
	}
	if !creds.ImageEnrollTokenInfo(ctx, img.ID).Exists {
		t.Fatal("token info")
	}
	t2, _ := creds.NewImageEnrollToken(ctx, img.ID)
	if creds.ValidImageEnrollToken(ctx, img.ID, t1) || !creds.ValidImageEnrollToken(ctx, img.ID, t2) {
		t.Fatal("replacing didn't invalidate older media")
	}
	if err := creds.RevokeImageEnrollToken(ctx, img.ID); err != nil {
		t.Fatal(err)
	}
	if creds.ValidImageEnrollToken(ctx, img.ID, t2) || creds.ImageEnrollTokenInfo(ctx, img.ID).Exists {
		t.Fatal("revoked token still valid")
	}
	t3, _ := creds.EnsureImageEnrollToken(ctx, img.ID)
	if t3 == t2 || !creds.ValidImageEnrollToken(ctx, img.ID, t3) {
		t.Fatal("export after revoke must carry a new token")
	}

	if n, _ := creds.LegacyMachineCount(ctx); n != 1 {
		t.Fatalf("legacy count = %d", n)
	}
	_, _ = creds.Issue(ctx, m, "approved", false)
	if n, _ := creds.LegacyMachineCount(ctx); n != 0 {
		t.Fatalf("legacy count after issue = %d", n)
	}
}

func TestAgentCredentialApproveAllPending(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	creds := NewAgentCredentialRepo(db)
	a := seedMachine(t, inv, "aa-1", "A", "")
	b := seedMachine(t, inv, "bb-1", "B", "")
	ra, _, _ := creds.RequestCredential(ctx, a, "nonce-a-111111111111111111111111111111", "", "")
	_, _, _ = creds.RequestCredential(ctx, b, "nonce-b-111111111111111111111111111111", "", "")
	_, _, _ = creds.RequestCredential(ctx, b, "nonce-b-222222222222222222222222222222", "", "")
	n, skipped, err := creds.ApproveAllPending(ctx, "admin")
	if err != nil || n != 1 || skipped != 1 {
		t.Fatalf("approve all = %d approved, %d skipped, %v", n, skipped, err)
	}
	if got, _, _ := creds.RequestCredential(ctx, a, "nonce-a-111111111111111111111111111111", "", ""); got.ID != ra.ID || got.Status != CredRequestApproved {
		t.Fatalf("single request not approved: %+v", got)
	}
	if c, _ := creds.PendingRequestCount(ctx); c != 2 {
		t.Fatalf("competing requests should stay pending, %d pending", c)
	}
}

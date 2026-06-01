package payload

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestServeUnattendSuppressesJoinForAgentImage verifies that the legacy
// <UnattendedJoin> block is emitted normally, but dropped once the image is
// configured to join AD via the agent (so Setup doesn't also attempt an
// online join during specialize).
func TestServeUnattendSuppressesJoinForAgentImage(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	isos := model.NewISORepo(db)
	unattendRepo := model.NewUnattendRepo(db)
	images := model.NewImageRepo(db)
	dj := model.NewDomainJoinRepo(db, bx)

	ua, err := unattendRepo.Create(ctx, model.Unattend{
		Name: "win-domain",
		SettingsJSON: `{"target_os":"windows-10","domain_join":{` +
			`"domain":"corp.example.com","join_user":"CORP\\join","join_password":"pw"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Create(ctx, model.Image{Name: "Win10", UnattendID: &ua.ID})
	if err != nil {
		t.Fatal(err)
	}

	svc := &Service{
		Blobs:      blobs,
		ISOs:       isos,
		Resolver:   resolve.New(images, isos, unattendRepo),
		DomainJoin: dj,
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fetch := func() string {
		resp, err := http.Get(srv.URL + "/payload/unattend/" + idStr2(img.ID))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// Legacy path: agent join not configured -> the unattend join block is present.
	if !strings.Contains(fetch(), "Microsoft-Windows-UnattendedJoin") {
		t.Error("expected UnattendedJoin block when agent join is not enabled")
	}

	// Enable agent-driven join on the image -> the block is suppressed.
	if err := dj.Set(ctx, model.ImageDomainJoin{
		ImageID: img.ID, Enabled: true, Domain: "corp.example.com", JoinUser: "CORP\\join",
	}, "pw"); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); strings.Contains(got, "Microsoft-Windows-UnattendedJoin") {
		t.Errorf("UnattendedJoin block should be suppressed for an agent-join image:\n%s", got)
	}
}

package branding

import (
	"context"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestSafeLogoURL(t *testing.T) {
	good := []string{
		"",
		"data:image/png;base64,iVBORw0KGgo=",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"https://cdn.example.com/logo.png",
		"http://intranet/logo.svg",
		"  data:image/png;base64,AAAA  ", // trimmed
	}
	for _, s := range good {
		if v, ok := SafeLogoURL(s); !ok {
			t.Errorf("SafeLogoURL(%q) = (%q,false), want acceptable", s, v)
		}
	}
	bad := []string{
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD4=",
		"data:application/json,{}",
		"ftp://example.com/logo.png",
		"//evil.example.com/x.png",
		"<script>",
	}
	for _, s := range bad {
		if v, ok := SafeLogoURL(s); ok {
			t.Errorf("SafeLogoURL(%q) = (%q,true), want rejected", s, v)
		}
	}
	if _, ok := SafeLogoURL("data:image/png;base64," + strings.Repeat("A", maxLogoBytes)); ok {
		t.Error("oversized logo should be rejected")
	}
}

func TestSetNormalisesLogo(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := New(db)

	// An unsafe logo is dropped rather than persisted.
	if err := r.Set(ctx, Brand{ProductName: "X", LogoDataURL: "javascript:alert(1)"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx); got.LogoDataURL != "" {
		t.Errorf("unsafe logo persisted: %q", got.LogoDataURL)
	}

	// A valid image data URL round-trips intact.
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	if err := r.Set(ctx, Brand{ProductName: "X", LogoDataURL: dataURL}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx); got.LogoDataURL != dataURL {
		t.Errorf("valid logo not preserved: got %q", got.LogoDataURL)
	}
}

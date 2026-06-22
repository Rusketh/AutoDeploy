package ui

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestDecodeLogo(t *testing.T) {
	// Build a real PNG, wrap it as a data URL, and round-trip it.
	var buf bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{1, 2, 3, 255})
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	if img := DecodeLogo(dataURL); img == nil {
		t.Error("valid PNG data URL should decode to an image")
	}

	// Everything else is skipped (returns nil) so the caller draws no logo.
	bad := []string{
		"",
		"   ",
		"not a url",
		"https://example.com/logo.png", // not a data URL
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", // SVG: no Go decoder
		"data:image/png;base64,$$$not-base64$$$",     // bad base64
		"data:text/plain;base64,aGVsbG8=",            // not an image type
	}
	for _, s := range bad {
		if img := DecodeLogo(s); img != nil {
			t.Errorf("DecodeLogo(%q) = non-nil, want nil", s)
		}
	}
}

func TestDrawLogoNilIsNoop(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if got := drawLogo(dst, nil, 50, 10, 80, 40); got != 10 {
		t.Errorf("drawLogo(nil) returned %d, want top unchanged (10)", got)
	}
}

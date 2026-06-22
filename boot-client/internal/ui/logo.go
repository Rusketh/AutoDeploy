package ui

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"  // register GIF decoder for DecodeLogo
	_ "image/jpeg" // register JPEG decoder for DecodeLogo
	_ "image/png"  // register PNG decoder for DecodeLogo
	"strings"

	xdraw "golang.org/x/image/draw"
)

// DecodeLogo decodes a base64 image data URL (e.g. "data:image/png;base64,…")
// into an image for the boot UI header. It returns nil for an empty value, a
// non-data URL, an unsupported format (notably SVG, which Go has no decoder
// for), or any decode error — the caller then simply draws no logo. PNG, JPEG
// and GIF are supported.
func DecodeLogo(dataURL string) image.Image {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:image/") {
		return nil
	}
	const marker = "base64,"
	i := strings.Index(dataURL, marker)
	if i < 0 {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataURL[i+len(marker):]))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return img
}

// drawLogo scales logo to fit within (maxW × maxH) preserving aspect, and blits
// it centred horizontally on centerX with its top edge at top, compositing over
// whatever is already there (so a transparent PNG shows the background). It
// returns the bottom y of the drawn logo, or top unchanged when logo is nil.
func drawLogo(dst *image.RGBA, logo image.Image, centerX, top, maxW, maxH int) int {
	if logo == nil {
		return top
	}
	src := logo.Bounds()
	sw, sh := src.Dx(), src.Dy()
	if sw <= 0 || sh <= 0 {
		return top
	}
	h := maxH
	w := sw * h / sh
	if w > maxW {
		w = maxW
		h = sh * w / sw
	}
	if w <= 0 || h <= 0 {
		return top
	}
	x0 := centerX - w/2
	xdraw.CatmullRom.Scale(dst, image.Rect(x0, top, x0+w, top+h), logo, src, xdraw.Over, nil)
	return top + h
}

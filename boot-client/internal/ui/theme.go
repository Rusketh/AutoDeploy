// Package ui renders the AutoDeploy boot experience to an in-memory RGBA
// image (which the framebuffer backend blits to the screen) and turns the
// neutral input stream into navigation. It is deliberately
// rendering-agnostic: screens draw into a *image.RGBA and react to
// input.Event values, so the layout/state logic is unit-tested without a
// real framebuffer or input device.
package ui

import (
	"image"
	"image/color"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Theme holds colours + fonts. Colours derive from the operator's branding
// (primary colour); everything else is a neutral dark palette chosen for
// legibility on any panel.
type Theme struct {
	Bg        color.RGBA
	Panel     color.RGBA
	Text      color.RGBA
	Muted     color.RGBA
	Primary   color.RGBA
	OnPrimary color.RGBA
	Danger    color.RGBA

	Title   font.Face
	Body    font.Face
	Small   font.Face
}

// NewTheme builds a theme from a primary colour (CSS hex like "#0b65c2";
// empty/invalid falls back to a default blue) at the given UI scale.
func NewTheme(primaryHex string, scale float64) (*Theme, error) {
	primary := parseHexColor(primaryHex, color.RGBA{0x0b, 0x65, 0xc2, 0xff})
	reg, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	face := func(f *opentype.Font, pt float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: pt * scale, DPI: 72, Hinting: font.HintingFull})
	}
	title, err := face(bold, 34)
	if err != nil {
		return nil, err
	}
	body, err := face(reg, 20)
	if err != nil {
		return nil, err
	}
	small, err := face(reg, 15)
	if err != nil {
		return nil, err
	}
	return &Theme{
		Bg:        color.RGBA{0x10, 0x14, 0x1a, 0xff},
		Panel:     color.RGBA{0x1b, 0x22, 0x2c, 0xff},
		Text:      color.RGBA{0xf2, 0xf5, 0xf8, 0xff},
		Muted:     color.RGBA{0x9a, 0xa6, 0xb2, 0xff},
		Primary:   primary,
		OnPrimary: color.RGBA{0xff, 0xff, 0xff, 0xff},
		Danger:    color.RGBA{0xd0, 0x45, 0x3c, 0xff},
		Title:     title, Body: body, Small: small,
	}, nil
}

// drawText renders s at baseline (x,y) in face/col and returns the advance
// width in pixels.
func drawText(dst *image.RGBA, face font.Face, col color.RGBA, x, y int, s string) int {
	d := &font.Drawer{
		Dst: dst, Src: image.NewUniform(col), Face: face,
		Dot: fixed.P(x, y),
	}
	d.DrawString(s)
	return d.Dot.X.Round() - x
}

// textWidth measures s in face without drawing.
func textWidth(face font.Face, s string) int {
	d := &font.Drawer{Face: face}
	return d.MeasureString(s).Round()
}

// parseHexColor parses "#rrggbb" / "rrggbb"; returns def on failure.
func parseHexColor(s string, def color.RGBA) color.RGBA {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) != 6 {
		return def
	}
	v := make([]byte, 3)
	for i := 0; i < 3; i++ {
		hi, ok1 := hexNibble(s[i*2])
		lo, ok2 := hexNibble(s[i*2+1])
		if !ok1 || !ok2 {
			return def
		}
		v[i] = hi<<4 | lo
	}
	return color.RGBA{v[0], v[1], v[2], 0xff}
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

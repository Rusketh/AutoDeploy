package ui

import (
	"image"
	"image/color"
	"image/draw"
)

// fillRect paints r with col.
func fillRect(dst *image.RGBA, r image.Rectangle, col color.RGBA) {
	draw.Draw(dst, r, image.NewUniform(col), image.Point{}, draw.Src)
}

// fillRoundRect paints r with col and approximate rounded corners of the
// given radius (corner pixels outside the rounded region are left
// untouched). Cheap per-pixel corner test -- fine at boot-UI sizes.
func fillRoundRect(dst *image.RGBA, r image.Rectangle, radius int, col color.RGBA) {
	if radius <= 0 {
		fillRect(dst, r, col)
		return
	}
	if radius*2 > r.Dx() {
		radius = r.Dx() / 2
	}
	if radius*2 > r.Dy() {
		radius = r.Dy() / 2
	}
	// Center band (full width) + side bands handled by the corner test.
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if insideRounded(x, y, r, radius) {
				dst.SetRGBA(x, y, col)
			}
		}
	}
}

func insideRounded(x, y int, r image.Rectangle, rad int) bool {
	// Determine the nearest corner centre if the pixel is in a corner box.
	cx, cy := x, y
	inCorner := false
	switch {
	case x < r.Min.X+rad && y < r.Min.Y+rad:
		cx, cy, inCorner = r.Min.X+rad, r.Min.Y+rad, true
	case x >= r.Max.X-rad && y < r.Min.Y+rad:
		cx, cy, inCorner = r.Max.X-rad-1, r.Min.Y+rad, true
	case x < r.Min.X+rad && y >= r.Max.Y-rad:
		cx, cy, inCorner = r.Min.X+rad, r.Max.Y-rad-1, true
	case x >= r.Max.X-rad && y >= r.Max.Y-rad:
		cx, cy, inCorner = r.Max.X-rad-1, r.Max.Y-rad-1, true
	}
	if !inCorner {
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= rad*rad
}

// strokeRect draws a 1px border in col.
func strokeRect(dst *image.RGBA, r image.Rectangle, col color.RGBA) {
	for x := r.Min.X; x < r.Max.X; x++ {
		dst.SetRGBA(x, r.Min.Y, col)
		dst.SetRGBA(x, r.Max.Y-1, col)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		dst.SetRGBA(r.Min.X, y, col)
		dst.SetRGBA(r.Max.X-1, y, col)
	}
}

// drawCursor draws a simple arrow-ish pointer at (x,y).
func drawCursor(dst *image.RGBA, x, y int, col color.RGBA) {
	for i := 0; i < 14; i++ {
		for j := 0; j <= i/2; j++ {
			dst.SetRGBA(x+j, y+i, col)
		}
	}
}

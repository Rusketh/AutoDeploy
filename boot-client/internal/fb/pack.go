package fb

import "image"

// packPixels converts an RGBA backbuffer into the framebuffer's native
// pixel format and writes it into dst (the mmap'd device memory). It
// supports 32/24/16 bpp using the device's reported channel bit offsets
// and lengths, little-endian (every real efifb/simpledrm/hyperv_fb/vmwgfx
// surface). Kept separate from device I/O so it is unit-tested without a
// real framebuffer.
func packPixels(dst []byte, src *image.RGBA, w, h, bpp, lineLength int,
	rOff, gOff, bOff, rLen, gLen, bLen uint32) {
	bytesPP := bpp / 8
	for y := 0; y < h; y++ {
		rowStart := y * lineLength
		srcRow := src.PixOffset(0, y)
		for x := 0; x < w; x++ {
			si := srcRow + x*4
			if si+3 >= len(src.Pix) {
				continue
			}
			r := uint32(src.Pix[si])
			g := uint32(src.Pix[si+1])
			b := uint32(src.Pix[si+2])
			// Scale 8-bit channels down to the device channel widths and
			// shift into place.
			val := (scaleChannel(r, rLen) << rOff) |
				(scaleChannel(g, gLen) << gOff) |
				(scaleChannel(b, bLen) << bOff)
			di := rowStart + x*bytesPP
			if di+bytesPP > len(dst) {
				continue
			}
			switch bytesPP {
			case 4:
				dst[di] = byte(val)
				dst[di+1] = byte(val >> 8)
				dst[di+2] = byte(val >> 16)
				dst[di+3] = byte(val >> 24)
			case 3:
				dst[di] = byte(val)
				dst[di+1] = byte(val >> 8)
				dst[di+2] = byte(val >> 16)
			case 2:
				dst[di] = byte(val)
				dst[di+1] = byte(val >> 8)
			}
		}
	}
}

// scaleChannel maps an 8-bit channel value to a field of the given bit
// length (e.g. 8->5 for RGB565's red/blue). length 0 or >=8 passes the
// 8-bit value through (masked to length when <8).
func scaleChannel(v8, length uint32) uint32 {
	switch {
	case length == 0 || length >= 8:
		return v8 & 0xFF
	default:
		max := uint32((1 << length) - 1)
		return (v8 * max) / 255
	}
}

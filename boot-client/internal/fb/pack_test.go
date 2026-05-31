package fb

import (
	"image"
	"testing"
)

func TestPackPixels32bpp(t *testing.T) {
	// 2x1 image, XRGB8888 (R@16,G@8,B@0, all 8-bit), 32bpp.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Pix[0], src.Pix[1], src.Pix[2], src.Pix[3] = 0x12, 0x34, 0x56, 0xff // px0
	src.Pix[4], src.Pix[5], src.Pix[6], src.Pix[7] = 0xff, 0x00, 0x00, 0xff // px1 red
	dst := make([]byte, 2*4)
	packPixels(dst, src, 2, 1, 32, 8, /*line*/
		16, 8, 0, 8, 8, 8)
	// px0: val = R<<16|G<<8|B = 0x123456 -> LE bytes 56 34 12 00
	if dst[0] != 0x56 || dst[1] != 0x34 || dst[2] != 0x12 {
		t.Errorf("px0 = % x, want 56 34 12 00", dst[0:4])
	}
	// px1 red: 0xff0000 -> 00 00 ff 00
	if dst[4] != 0x00 || dst[5] != 0x00 || dst[6] != 0xff {
		t.Errorf("px1 = % x, want 00 00 ff 00", dst[4:8])
	}
}

func TestPackPixels16bppRGB565(t *testing.T) {
	// RGB565: R@11 len5, G@5 len6, B@0 len5.
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.Pix[0], src.Pix[1], src.Pix[2], src.Pix[3] = 0xff, 0xff, 0xff, 0xff // white
	dst := make([]byte, 2)
	packPixels(dst, src, 1, 1, 16, 2, 11, 5, 0, 5, 6, 5)
	// white -> all field bits set -> 0xFFFF
	if dst[0] != 0xff || dst[1] != 0xff {
		t.Errorf("white 565 = % x, want ff ff", dst)
	}
}

func TestScaleChannel(t *testing.T) {
	if got := scaleChannel(0xff, 5); got != 31 {
		t.Errorf("scale 255->5bit = %d, want 31", got)
	}
	if got := scaleChannel(0x00, 5); got != 0 {
		t.Errorf("scale 0->5bit = %d, want 0", got)
	}
	if got := scaleChannel(0xff, 8); got != 0xff {
		t.Errorf("scale 255->8bit = %d, want 255", got)
	}
}

package ui

import (
	"image"
	"image/color"
	"strings"
	"time"

	"golang.org/x/image/font"
)

// Identity is what the imaging screens print about the machine in their
// header band, so a technician (or a photo of the screen) ties what's on
// it to the right machine in the portal. Every field is optional; empty
// ones are left out.
type Identity struct {
	Name   string // computer name the server knows the machine by
	Model  string // manufacturer + product, e.g. "Dell Inc. OptiPlex 7090"
	Serial string // SMBIOS serial number
	IP     string // this client's IPv4 address
	Server string // AutoDeploy server host[:port] (no scheme/path)
}

// primary is the header's first line: the machine name when known,
// otherwise the model, otherwise a neutral label.
func (id Identity) primary() string {
	switch {
	case id.Name != "":
		return id.Name
	case id.Model != "":
		return id.Model
	}
	return "This machine"
}

// details is the header's second line: the facts not already shown as
// the primary label, joined with a middle dot. Ordered by usefulness, as
// a narrow panel truncates from the end: serial, IP, server, then model.
func (id Identity) details() string {
	var parts []string
	if id.Serial != "" {
		parts = append(parts, "S/N "+id.Serial)
	}
	if id.IP != "" {
		parts = append(parts, "IP "+id.IP)
	}
	if id.Server != "" {
		parts = append(parts, "Server "+id.Server)
	}
	if id.Name != "" && id.Model != "" {
		parts = append(parts, id.Model)
	}
	return strings.Join(parts, "  ·  ")
}

// headerPad is the inner padding of the identity header band.
const headerPad = 14

// lineHeight is face's recommended line spacing in pixels.
func lineHeight(face font.Face) int {
	return face.Metrics().Height.Ceil()
}

// headerHeight is the height drawHeader paints for th.
func headerHeight(th *Theme) int {
	return headerPad*2 + lineHeight(th.Body) + lineHeight(th.Small)
}

// drawHeader paints the identity band across the top of b: the primary
// label and detail line on the left, and up to two right-aligned status
// lines (e.g. "Step 3 of 6" / "Elapsed 04:12") on the right. The left
// side is truncated with an ellipsis rather than ever running under the
// right side, so it stays legible at 800x600. Returns the band's bottom y.
func drawHeader(img *image.RGBA, th *Theme, b image.Rectangle, id Identity, right1, right2 string, right1Col color.RGBA) int {
	h := headerHeight(th)
	band := image.Rect(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+h)
	fillRect(img, band, th.Panel)
	x := b.Min.X + 20
	base1 := b.Min.Y + headerPad + th.Body.Metrics().Ascent.Ceil()
	base2 := base1 + lineHeight(th.Small) + 4

	rw := maxInt(textWidth(th.Body, right1), textWidth(th.Small, right2))
	rx := b.Max.X - 20
	if right1 != "" {
		drawText(img, th.Body, right1Col, rx-textWidth(th.Body, right1), base1, right1)
	}
	if right2 != "" {
		drawText(img, th.Small, th.Muted, rx-textWidth(th.Small, right2), base2, right2)
	}
	leftW := rx - x
	if rw > 0 {
		leftW -= rw + 24
	}
	drawText(img, th.Body, th.Text, x, base1, ellipsize(th.Body, id.primary(), leftW))
	if d := id.details(); d != "" {
		drawText(img, th.Small, th.Muted, x, base2, ellipsize(th.Small, d, leftW))
	}
	return band.Max.Y
}

// ellipsize shortens s (with a trailing "…") until it fits maxW pixels
// in face. Returns s unchanged when it already fits; "" when even the
// ellipsis doesn't.
func ellipsize(face font.Face, s string, maxW int) string {
	if textWidth(face, s) <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 0 {
		r = r[:len(r)-1]
		cand := strings.TrimRight(string(r), " ·") + "…"
		if textWidth(face, cand) <= maxW {
			return cand
		}
	}
	return ""
}

// wrapText splits s into lines no wider than maxW in face, breaking at
// spaces (an over-long single word is hard-broken by ellipsize). At most
// maxLines lines are returned; if s needed more, the last one ends in "…".
func wrapText(face font.Face, s string, maxW, maxLines int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		cur := ""
		for _, w := range words {
			cand := w
			if cur != "" {
				cand = cur + " " + w
			}
			if textWidth(face, cand) <= maxW || cur == "" {
				cur = cand
				continue
			}
			lines = append(lines, cur)
			cur = w
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	for i := range lines {
		lines[i] = ellipsize(face, lines[i], maxW)
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = ellipsize(face, lines[maxLines-1]+" …", maxW)
	}
	return lines
}

// formatElapsed renders d as m:ss, or h:mm:ss from an hour up.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	h, m, sec := s/3600, (s/60)%60, s%60
	two := func(n int) string {
		if n < 10 {
			return "0" + itoa(n)
		}
		return itoa(n)
	}
	if h > 0 {
		return itoa(h) + ":" + two(m) + ":" + two(sec)
	}
	return itoa(m) + ":" + two(sec)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

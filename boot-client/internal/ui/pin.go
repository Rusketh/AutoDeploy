package ui

import (
	"image"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// PINScreen collects the access PIN. The entered value is masked on screen
// (dots). Digits/characters append; Backspace deletes; Enter submits; Esc
// cancels. An on-screen keypad lets a mouse-only user enter it too.
type PINScreen struct {
	Title   string
	Message string // error/status line (e.g. "Incorrect PIN, 2 tries left")
	Version string // boot-client build version, shown in the corner
	value   []rune
	maxLen  int

	padRects map[string]image.Rectangle // label -> rect, for mouse keypad
	submitR  image.Rectangle
	clearR   image.Rectangle

	Entered   string
	Submitted bool
	Cancelled bool
}

// NewPINScreen builds a PIN entry screen.
func NewPINScreen(title string) *PINScreen {
	return &PINScreen{Title: title, maxLen: 32, padRects: map[string]image.Rectangle{}}
}

// Value returns the current (unmasked) buffer -- for tests.
func (p *PINScreen) Value() string { return string(p.value) }

func (p *PINScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	fillRect(img, b, th.Bg)
	cx := b.Dx() / 2
	drawCentered(img, th.Title, th.Text, cx, b.Min.Y+90, p.Title)
	drawCentered(img, th.Body, th.Muted, cx, b.Min.Y+130, "Enter the AutoDeploy access PIN")

	// Masked field.
	fieldW, fieldH := 320, 56
	fr := image.Rect(cx-fieldW/2, b.Min.Y+170, cx+fieldW/2, b.Min.Y+170+fieldH)
	fillRoundRect(img, fr, 8, th.Panel)
	strokeRect(img, fr, th.Primary)
	dots := ""
	for range p.value {
		dots += "•"
	}
	if dots == "" {
		drawText(img, th.Body, th.Muted, fr.Min.X+16, fr.Min.Y+37, "PIN")
	} else {
		drawCentered(img, th.Title, th.Text, cx, fr.Min.Y+40, dots)
	}
	if p.Message != "" {
		drawCentered(img, th.Small, th.Danger, cx, fr.Max.Y+28, p.Message)
	}

	// On-screen keypad (mouse-only support).
	p.padRects = map[string]image.Rectangle{}
	keys := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}
	btn := 56
	gap := 10
	cols := 5
	gridW := cols*btn + (cols-1)*gap
	gx := cx - gridW/2
	gy := fr.Max.Y + 60
	for i, k := range keys {
		col := i % cols
		row := i / cols
		r := image.Rect(gx+col*(btn+gap), gy+row*(btn+gap), gx+col*(btn+gap)+btn, gy+row*(btn+gap)+btn)
		p.padRects[k] = r
		fillRoundRect(img, r, 8, th.Panel)
		drawCentered(img, th.Body, th.Text, (r.Min.X+r.Max.X)/2, r.Min.Y+37, k)
	}
	// Clear + Submit buttons.
	rowY := gy + 2*(btn+gap) + 10
	p.clearR = image.Rect(gx, rowY, gx+gridW/2-gap/2, rowY+btn)
	p.submitR = image.Rect(gx+gridW/2+gap/2, rowY, gx+gridW, rowY+btn)
	fillRoundRect(img, p.clearR, 8, th.Panel)
	drawCentered(img, th.Body, th.Text, (p.clearR.Min.X+p.clearR.Max.X)/2, p.clearR.Min.Y+37, "Clear")
	fillRoundRect(img, p.submitR, 8, th.Primary)
	drawCentered(img, th.Body, th.OnPrimary, (p.submitR.Min.X+p.submitR.Max.X)/2, p.submitR.Min.Y+37, "Unlock")

	drawCentered(img, th.Small, th.Muted, cx, b.Max.Y-40, "Type your PIN · Enter unlock · Esc cancel")
	drawVersion(img, th, b, p.Version)
}

func (p *PINScreen) Handle(ev input.Event) Action {
	switch ev.Type {
	case input.Rune:
		if len(p.value) < p.maxLen && ev.Rune >= ' ' {
			p.value = append(p.value, ev.Rune)
			return ActionRedraw
		}
	case input.KeyPress:
		switch ev.Key {
		case input.KeyBackspace:
			if len(p.value) > 0 {
				p.value = p.value[:len(p.value)-1]
				return ActionRedraw
			}
		case input.KeyEnter:
			return p.submit()
		case input.KeyEscape:
			p.Cancelled = true
			return ActionCancel
		}
	case input.PointerDown:
		for k, r := range p.padRects {
			if pointIn(ev.X, ev.Y, r) {
				if len(p.value) < p.maxLen {
					p.value = append(p.value, rune(k[0]))
				}
				return ActionRedraw
			}
		}
		if pointIn(ev.X, ev.Y, p.clearR) {
			p.value = p.value[:0]
			return ActionRedraw
		}
		if pointIn(ev.X, ev.Y, p.submitR) {
			return p.submit()
		}
	}
	return ActionNone
}

func (p *PINScreen) submit() Action {
	if len(p.value) == 0 {
		return ActionNone
	}
	p.Entered = string(p.value)
	p.Submitted = true
	return ActionDone
}

func pointIn(x, y int, r image.Rectangle) bool {
	return x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y
}

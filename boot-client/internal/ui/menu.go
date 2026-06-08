package ui

import (
	"image"
	"image/color"

	"golang.org/x/image/font"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// MenuItem is one selectable row.
type MenuItem struct {
	Title string
	Desc  string
}

// MenuScreen is the deploy/re-image selection list. Keyboard: up/down to
// move, enter to choose, space to toggle the progress checkbox, esc to
// cancel. Mouse: hover highlights, click chooses, click the checkbox to
// toggle it. Selected index is read after Run returns ActionDone; Cancelled
// is true on ActionCancel.
type MenuScreen struct {
	Title    string
	Subtitle string
	Items    []MenuItem
	sel      int
	rowRects []image.Rectangle // computed each Draw, used for hit-testing
	cbRect   image.Rectangle   // checkbox hit area (box + label), computed each Draw

	// ShowProgress is the state of the "Show imaging progress" checkbox.
	// Ticked (true) by default, which keeps the normal graphical progress
	// screen during imaging. Unticking it hands the screen back to the text
	// console for the deploy so kernel and boot-client logs are visible on
	// the machine -- the way to debug a deploy that stalls before it can ship
	// any logs to the portal (e.g. a USB NIC that wedges the download). Read
	// after Run returns.
	ShowProgress bool
	// Version is the boot-client build version, shown in the corner so the
	// operator can confirm which boot package the machine loaded.
	Version string

	Chosen    int
	Cancelled bool
}

// NewMenuScreen builds a menu. The progress checkbox starts ticked.
func NewMenuScreen(title, subtitle string, items []MenuItem) *MenuScreen {
	return &MenuScreen{Title: title, Subtitle: subtitle, Items: items, Chosen: -1, ShowProgress: true}
}

// Selected returns the current highlighted index (for tests / callers).
func (m *MenuScreen) Selected() int { return m.sel }

func (m *MenuScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	fillRect(img, b, th.Bg)
	cx := b.Dx() / 2
	// Header band.
	drawCentered(img, th.Title, th.Text, cx, b.Min.Y+90, m.Title)
	if m.Subtitle != "" {
		drawCentered(img, th.Body, th.Muted, cx, b.Min.Y+130, m.Subtitle)
	}
	// Rows.
	rowW := minInt(720, b.Dx()-120)
	rowH := 64
	gap := 14
	x0 := b.Min.X + (b.Dx()-rowW)/2
	y := b.Min.Y + 190
	m.rowRects = m.rowRects[:0]
	for i, it := range m.Items {
		r := image.Rect(x0, y, x0+rowW, y+rowH)
		m.rowRects = append(m.rowRects, r)
		bg := th.Panel
		if i == m.sel {
			bg = th.Primary
		}
		fillRoundRect(img, r, 10, bg)
		txtCol := th.Text
		descCol := th.Muted
		if i == m.sel {
			txtCol = th.OnPrimary
			descCol = th.OnPrimary
		}
		drawText(img, th.Body, txtCol, r.Min.X+20, r.Min.Y+28, it.Title)
		if it.Desc != "" {
			drawText(img, th.Small, descCol, r.Min.X+20, r.Min.Y+50, it.Desc)
		}
		y += rowH + gap
	}

	// "Show imaging progress" checkbox, left-aligned under the rows. Ticked
	// (default) keeps the graphical progress screen; unticking it hands the
	// screen to the console during imaging so logs are visible for debugging.
	m.drawCheckbox(img, th, x0, y+6)

	drawCentered(img, th.Small, th.Muted, cx, b.Max.Y-40,
		"↑/↓ select · Enter deploy · Space toggle progress · Esc cancel")
	drawVersion(img, th, b, m.Version)
}

// drawCheckbox renders the "Show imaging progress" toggle at (x,y) and records
// its hit area (box + label) in cbRect. A filled box reads as ticked, an
// outline-only box as unticked -- so it never depends on a check-mark glyph
// being present in the bundled font.
func (m *MenuScreen) drawCheckbox(img *image.RGBA, th *Theme, x, y int) {
	const boxSz = 24
	box := image.Rect(x, y, x+boxSz, y+boxSz)
	fillRoundRect(img, box, 5, th.Panel)
	strokeRect(img, box, th.Primary)
	if m.ShowProgress {
		inset := image.Rect(box.Min.X+5, box.Min.Y+5, box.Max.X-5, box.Max.Y-5)
		fillRoundRect(img, inset, 3, th.Primary)
	}
	label := "Show imaging progress"
	lx := box.Max.X + 12
	drawText(img, th.Body, th.Text, lx, box.Min.Y+18, label)
	drawText(img, th.Small, th.Muted, box.Min.X, box.Max.Y+22,
		"Uncheck to watch the console while imaging (for debugging)")
	lw := textWidth(th.Body, label)
	m.cbRect = image.Rect(box.Min.X, box.Min.Y, lx+lw, box.Max.Y)
}

func (m *MenuScreen) Handle(ev input.Event) Action {
	switch ev.Type {
	case input.Rune:
		// Space toggles the "Show imaging progress" checkbox.
		if ev.Rune == ' ' {
			m.ShowProgress = !m.ShowProgress
			return ActionRedraw
		}
	case input.KeyPress:
		switch ev.Key {
		case input.KeyUp:
			if m.sel > 0 {
				m.sel--
				return ActionRedraw
			}
		case input.KeyDown:
			if m.sel < len(m.Items)-1 {
				m.sel++
				return ActionRedraw
			}
		case input.KeyEnter:
			m.Chosen = m.sel
			return ActionDone
		case input.KeyEscape:
			m.Cancelled = true
			return ActionCancel
		}
	case input.PointerMove:
		if i := m.hit(ev.X, ev.Y); i >= 0 && i != m.sel {
			m.sel = i
			return ActionRedraw
		}
	case input.PointerDown:
		// Clicking the checkbox toggles it -- it must not choose an image.
		if pointIn(ev.X, ev.Y, m.cbRect) {
			m.ShowProgress = !m.ShowProgress
			return ActionRedraw
		}
		if i := m.hit(ev.X, ev.Y); i >= 0 {
			m.sel = i
			m.Chosen = i
			return ActionDone
		}
	}
	return ActionNone
}

func (m *MenuScreen) hit(x, y int) int {
	for i, r := range m.rowRects {
		if x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y {
			return i
		}
	}
	return -1
}

func drawCentered(img *image.RGBA, face font.Face, col color.RGBA, cx, baseline int, s string) {
	w := textWidth(face, s)
	drawText(img, face, col, cx-w/2, baseline, s)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

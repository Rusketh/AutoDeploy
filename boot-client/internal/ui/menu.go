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
	scroll   int               // index of the first visible row (windowed list)
	rowFirst int               // item index of rowRects[0], for hit-testing
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
	// Header band. Default offsets; an operator logo, when present, sits above
	// the title and pushes the title/subtitle/rows down so nothing overlaps.
	titleY := b.Min.Y + 90
	subY := b.Min.Y + 130
	rowsY := b.Min.Y + 190
	if th.Logo != nil {
		logoBottom := drawLogo(img, th.Logo, cx, b.Min.Y+28, minInt(420, b.Dx()-160), 72)
		titleY = logoBottom + 46
		subY = titleY + 34
		rowsY = titleY + 96
	}
	drawCentered(img, th.Title, th.Text, cx, titleY, m.Title)
	if m.Subtitle != "" {
		drawCentered(img, th.Body, th.Muted, cx, subY, m.Subtitle)
	}
	// Rows.
	rowW := minInt(720, b.Dx()-120)
	rowH := 64
	gap := 14
	perRow := rowH + gap
	x0 := b.Min.X + (b.Dx()-rowW)/2

	// Footer band pinned to the bottom: the progress checkbox and keyboard
	// help live here at a FIXED position so a long list can never push them
	// off the screen or paint over them. The row list is confined to the
	// space above this band.
	const footerH = 120
	footerTop := b.Max.Y - footerH

	// Window the list to the rows band, reserving one line at the top and
	// bottom for the "more above / more below" scroll hints. maxVisible is how
	// many rows fit; a list longer than that scrolls to keep the selection in
	// view rather than overflowing the screen.
	const hintH = 22
	bandTop := rowsY
	bandBottom := footerTop - 8
	maxVisible := (bandBottom - bandTop - 2*hintH) / perRow
	if maxVisible < 1 {
		maxVisible = 1
	}
	m.scroll = clampScroll(m.scroll, m.sel, maxVisible, len(m.Items))

	first := m.scroll
	last := minInt(first+maxVisible, len(m.Items))
	m.rowFirst = first

	if first > 0 {
		drawCentered(img, th.Small, th.Muted, cx, bandTop+hintH-8, "↑ more above")
	}

	y := bandTop + hintH
	m.rowRects = m.rowRects[:0]
	for i := first; i < last; i++ {
		it := m.Items[i]
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
		y += perRow
	}

	if last < len(m.Items) {
		drawCentered(img, th.Small, th.Muted, cx, y+hintH-8, "↓ more below")
	}

	// "Show imaging progress" checkbox, pinned to the footer band. Ticked
	// (default) keeps the graphical progress screen; unticking it hands the
	// screen to the console during imaging so logs are visible for debugging.
	m.drawCheckbox(img, th, x0, footerTop+10)

	drawCentered(img, th.Small, th.Muted, cx, b.Max.Y-40,
		"↑/↓ select · Enter deploy · Space toggle progress · Esc cancel")
	drawVersion(img, th, b, m.Version)
}

// clampScroll returns a scroll offset (index of the first visible row) that
// keeps the selected row within a window of maxVisible rows, and never scrolls
// past the end of an n-item list.
func clampScroll(scroll, sel, maxVisible, n int) int {
	if sel < scroll {
		scroll = sel
	}
	if sel >= scroll+maxVisible {
		scroll = sel - maxVisible + 1
	}
	maxScroll := n - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
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
			return m.rowFirst + i
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

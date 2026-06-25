package ui

import (
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// testTheme builds a real theme (exercises font loading + colour parse).
func testTheme(t *testing.T) *Theme {
	t.Helper()
	th, err := NewTheme("#0b65c2", 1.0)
	if err != nil {
		t.Fatalf("NewTheme: %v", err)
	}
	return th
}

func TestParseHexColor(t *testing.T) {
	got := parseHexColor("#ff8800", color.RGBA{})
	if got != (color.RGBA{0xff, 0x88, 0x00, 0xff}) {
		t.Errorf("parseHexColor = %v", got)
	}
	def := color.RGBA{1, 2, 3, 4}
	if parseHexColor("nope", def) != def {
		t.Error("invalid hex should return default")
	}
	if parseHexColor("0b65c2", def) != (color.RGBA{0x0b, 0x65, 0xc2, 0xff}) {
		t.Error("no-hash form should parse")
	}
}

// drawInto renders a screen into a fresh image so Draw is exercised (must
// not panic, and must put SOME non-background pixels down).
func drawInto(t *testing.T, scr Screen, th *Theme) *image.RGBA {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	scr.Draw(img, th, img.Bounds())
	return img
}

func TestMenuKeyboardNavigation(t *testing.T) {
	th := testTheme(t)
	m := NewMenuScreen("Deploy", "pick one", []MenuItem{
		{Title: "Win11 Lab"}, {Title: "Win11 Office"}, {Title: "Server 2022"},
	})
	drawInto(t, m, th) // compute row rects
	if m.Selected() != 0 {
		t.Fatalf("initial sel = %d", m.Selected())
	}
	m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyDown})
	m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyDown})
	if m.Selected() != 2 {
		t.Errorf("after 2x down sel = %d, want 2", m.Selected())
	}
	// Can't go past the end.
	if a := m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyDown}); a != ActionNone {
		t.Errorf("down at end = %v, want None", a)
	}
	// Enter chooses.
	if a := m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyEnter}); a != ActionDone {
		t.Errorf("enter = %v, want Done", a)
	}
	if m.Chosen != 2 {
		t.Errorf("chosen = %d, want 2", m.Chosen)
	}
}

func TestMenuMouseClick(t *testing.T) {
	th := testTheme(t)
	m := NewMenuScreen("Deploy", "", []MenuItem{{Title: "A"}, {Title: "B"}})
	img := drawInto(t, m, th)
	_ = img
	// Click inside the second row's rect.
	r := m.rowRects[1]
	a := m.Handle(input.Event{Type: input.PointerDown, X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2})
	if a != ActionDone || m.Chosen != 1 {
		t.Errorf("click row 1: action=%v chosen=%d", a, m.Chosen)
	}
}

func TestMenuEscapeCancels(t *testing.T) {
	m := NewMenuScreen("X", "", []MenuItem{{Title: "A"}})
	if a := m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyEscape}); a != ActionCancel || !m.Cancelled {
		t.Errorf("escape: action=%v cancelled=%v", a, m.Cancelled)
	}
}

func TestMenuShowProgressToggle(t *testing.T) {
	th := testTheme(t)
	m := NewMenuScreen("Deploy", "pick one", []MenuItem{{Title: "A"}, {Title: "B"}})
	m.Version = "v1.2.3"
	if !m.ShowProgress {
		t.Fatalf("ShowProgress should default to true (ticked)")
	}
	drawInto(t, m, th) // computes cbRect
	// Space toggles it off without choosing or cancelling.
	if a := m.Handle(input.Event{Type: input.Rune, Rune: ' '}); a != ActionRedraw {
		t.Errorf("space action = %v, want Redraw", a)
	}
	if m.ShowProgress {
		t.Errorf("space should untick ShowProgress")
	}
	// A non-space rune is ignored.
	if a := m.Handle(input.Event{Type: input.Rune, Rune: 'x'}); a != ActionNone {
		t.Errorf("rune 'x' action = %v, want None", a)
	}
	// Clicking the checkbox re-ticks it -- and must NOT choose an image.
	r := m.cbRect
	a := m.Handle(input.Event{Type: input.PointerDown, X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2})
	if a != ActionRedraw {
		t.Errorf("checkbox click action = %v, want Redraw", a)
	}
	if !m.ShowProgress {
		t.Errorf("checkbox click should re-tick ShowProgress")
	}
	if m.Chosen != -1 {
		t.Errorf("checkbox click must not choose an image; Chosen=%d", m.Chosen)
	}
}

// TestMenuScrollsAndPinsFooter guards the deploy-menu layout: a list longer
// than the screen must window (scroll) to keep the selection visible, never
// render rows off the bottom, and never let the row list cover the pinned
// "Show imaging progress" checkbox.
func TestMenuScrollsAndPinsFooter(t *testing.T) {
	th := testTheme(t)
	items := make([]MenuItem, 40)
	for i := range items {
		items[i] = MenuItem{Title: fmt.Sprintf("Image %d", i), Desc: "role"}
	}
	m := NewMenuScreen("Deploy", "pick one", items)
	bounds := image.Rect(0, 0, 1024, 768)
	img := image.NewRGBA(bounds)
	m.Draw(img, th, bounds)

	// The list is windowed, not drawn in full.
	if n := len(m.rowRects); n == 0 || n >= len(items) {
		t.Fatalf("expected a windowed subset of rows, got %d of %d", n, len(items))
	}
	// Footer checkbox stays on screen and no row overlaps it.
	if m.cbRect.Max.Y > bounds.Max.Y {
		t.Errorf("checkbox runs off the bottom: cbRect=%v screen=%v", m.cbRect, bounds)
	}
	for _, r := range m.rowRects {
		if r.Max.Y > bounds.Max.Y {
			t.Errorf("row runs off the bottom of the screen: %v", r)
		}
		if r.Overlaps(m.cbRect) {
			t.Errorf("row %v covers the progress checkbox %v", r, m.cbRect)
		}
	}

	// Navigating far down scrolls the window so the selection stays visible.
	for i := 0; i < 30; i++ {
		m.Handle(input.Event{Type: input.KeyPress, Key: input.KeyDown})
	}
	m.Draw(img, th, bounds) // recompute scroll + visible rows
	if m.Selected() != 30 {
		t.Fatalf("after 30 downs sel=%d, want 30", m.Selected())
	}
	if m.sel < m.rowFirst || m.sel >= m.rowFirst+len(m.rowRects) {
		t.Errorf("selected row %d not in visible window [%d,%d)", m.sel, m.rowFirst, m.rowFirst+len(m.rowRects))
	}
	for _, r := range m.rowRects {
		if r.Max.Y > bounds.Max.Y || r.Overlaps(m.cbRect) {
			t.Errorf("after scroll, row %v off-screen or over the checkbox %v", r, m.cbRect)
		}
	}

	// Mouse hit-testing maps a visible row back to its true item index.
	lastLocal := len(m.rowRects) - 1
	r := m.rowRects[lastLocal]
	m.Handle(input.Event{Type: input.PointerDown, X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2})
	if want := m.rowFirst + lastLocal; m.Chosen != want {
		t.Errorf("click last visible row: Chosen=%d, want %d", m.Chosen, want)
	}
}

// TestScreensRenderVersion exercises the version footer on every imaging
// screen: Draw must not panic with a version set.
func TestScreensRenderVersion(t *testing.T) {
	th := testTheme(t)
	m := NewMenuScreen("Deploy", "", []MenuItem{{Title: "A"}})
	m.Version = "v9.9.9"
	drawInto(t, m, th)
	p := NewProgressScreen("Deploying")
	p.Version = "v9.9.9"
	drawInto(t, p, th)
	pin := NewPINScreen("Locked")
	pin.Version = "v9.9.9"
	drawInto(t, pin, th)
}

func TestPINEntryAndMasking(t *testing.T) {
	th := testTheme(t)
	p := NewPINScreen("Locked")
	p.Handle(input.Event{Type: input.Rune, Rune: '1'})
	p.Handle(input.Event{Type: input.Rune, Rune: '2'})
	p.Handle(input.Event{Type: input.Rune, Rune: '3'})
	if p.Value() != "123" {
		t.Fatalf("value = %q", p.Value())
	}
	p.Handle(input.Event{Type: input.KeyPress, Key: input.KeyBackspace})
	if p.Value() != "12" {
		t.Errorf("after backspace = %q", p.Value())
	}
	// Draw must show dots, never the digits.
	img := drawInto(t, p, th)
	_ = img
	a := p.Handle(input.Event{Type: input.KeyPress, Key: input.KeyEnter})
	if a != ActionDone || !p.Submitted || p.Entered != "12" {
		t.Errorf("submit: action=%v submitted=%v entered=%q", a, p.Submitted, p.Entered)
	}
}

func TestPINEmptySubmitIgnored(t *testing.T) {
	p := NewPINScreen("Locked")
	if a := p.Handle(input.Event{Type: input.KeyPress, Key: input.KeyEnter}); a != ActionNone {
		t.Errorf("empty submit = %v, want None", a)
	}
}

func TestPINMouseKeypad(t *testing.T) {
	th := testTheme(t)
	p := NewPINScreen("Locked")
	drawInto(t, p, th) // compute keypad rects
	r := p.padRects["7"]
	p.Handle(input.Event{Type: input.PointerDown, X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2})
	if p.Value() != "7" {
		t.Errorf("keypad click = %q, want 7", p.Value())
	}
	// Submit button.
	a := p.Handle(input.Event{Type: input.PointerDown, X: (p.submitR.Min.X + p.submitR.Max.X) / 2, Y: (p.submitR.Min.Y + p.submitR.Max.Y) / 2})
	if a != ActionDone || p.Entered != "7" {
		t.Errorf("submit click: action=%v entered=%q", a, p.Entered)
	}
}

func TestProgressDrawAndUpdate(t *testing.T) {
	th := testTheme(t)
	p := NewProgressScreen("Deploying")
	p.Set("Downloading media", "install.swm (2/3)", 66)
	st, det, file, pct := p.snapshot()
	if st != "Downloading media" || det != "install.swm (2/3)" || pct != 66 {
		t.Fatalf("snapshot = %q %q %q %d", st, det, file, pct)
	}
	// SetFile updates only the current-file line, leaving stage/detail/percent.
	p.SetFile("sources/install.swm")
	if st, det, file, pct = p.snapshot(); file != "sources/install.swm" {
		t.Fatalf("SetFile not reflected: file=%q", file)
	} else if st != "Downloading media" || det != "install.swm (2/3)" || pct != 66 {
		t.Fatalf("SetFile disturbed stage/detail/percent: %q %q %d", st, det, pct)
	}
	drawInto(t, p, th) // determinate, with a file line
	p.Set("Working", "", -1)
	drawInto(t, p, th) // indeterminate must also not panic
	// Input is ignored during progress.
	if a := p.Handle(input.Event{Type: input.KeyPress, Key: input.KeyEnter}); a != ActionNone {
		t.Errorf("progress input = %v, want None", a)
	}
}

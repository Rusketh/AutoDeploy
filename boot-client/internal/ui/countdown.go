package ui

import (
	"image"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// CountdownScreen warns the operator that the machine will hand control back to
// an in-progress Windows install in a few seconds, with a one-key override to
// stay in AutoDeploy instead. The caller owns the clock: it ticks the seconds
// down with SetRemaining + a repaint and enforces the timeout. Handle only
// reports the override keypress (Enter or Esc).
type CountdownScreen struct {
	Title   string
	Message string
	// Version is the boot-client build version, shown in the corner.
	Version string

	remaining int
}

// NewCountdownScreen builds the warning view with an initial seconds count.
func NewCountdownScreen(title, message string, seconds int) *CountdownScreen {
	return &CountdownScreen{Title: title, Message: message, remaining: seconds}
}

// SetRemaining updates the displayed seconds counter (the caller decrements it
// once a second and repaints).
func (s *CountdownScreen) SetRemaining(n int) {
	if n < 0 {
		n = 0
	}
	s.remaining = n
}

func (s *CountdownScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	fillRect(img, b, th.Bg)
	cx := b.Dx() / 2
	cy := b.Min.Y + b.Dy()/2
	drawCentered(img, th.Title, th.Text, cx, cy-120, s.Title)
	if s.Message != "" {
		drawCentered(img, th.Body, th.Text, cx, cy-60, s.Message)
	}
	// The big seconds counter.
	drawCentered(img, th.Title, th.Primary, cx, cy+10,
		"Booting Windows Setup in "+itoa(s.remaining)+"s")
	drawCentered(img, th.Small, th.Muted, cx, cy+70,
		"Press Enter to stay in AutoDeploy")
	drawVersion(img, th, b, s.Version)
}

// Handle returns ActionDone when the operator presses Enter or Esc (override:
// stay in AutoDeploy); every other key is ignored. The countdown timeout is the
// caller's responsibility, not the screen's.
func (s *CountdownScreen) Handle(ev input.Event) Action {
	if ev.Type == input.KeyPress && (ev.Key == input.KeyEnter || ev.Key == input.KeyEscape) {
		return ActionDone
	}
	return ActionNone
}

package ui

import (
	"image"
	"time"

	"golang.org/x/image/font"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// FailureChoice is what the technician picked on the failure screen.
type FailureChoice int

const (
	// FailureNone: no choice yet (or input went away).
	FailureNone FailureChoice = iota
	// FailureRetry: start the boot client over (back to the menu).
	FailureRetry
	// FailureReboot: restart the machine.
	FailureReboot
	// FailureShell: leave to the initramfs diagnostic shell.
	FailureShell
)

// FailureInfo is everything the failure screen shows.
type FailureInfo struct {
	// Title is the red headline; empty means "Deployment failed".
	Title string
	// Message is the plain-English explanation, e.g. "Couldn't prepare the
	// disk (partitioning failed): zap /dev/sda: exit status 2".
	Message string
	// Code is a short, searchable error code, e.g. "PARTITION".
	Code string
	// Identity is the machine identity shown in the header.
	Identity Identity
	// Output is the last few lines of the failing command's stderr, if any.
	Output []string
	// LogsShipped reports whether the run's logs reached the server.
	LogsShipped bool
	// Elapsed is how long the deploy ran before failing (0 hides it).
	Elapsed time.Duration
}

// FailureScreen is the red "Deployment failed" screen every boot-client
// failure path ends on: what went wrong in plain English, a short error
// code, the machine identity, the failing command's last output, where the
// logs went, and what the technician can do next. Handle maps R / Enter /
// Esc to retry / restart / shell.
type FailureScreen struct {
	Info FailureInfo
	// Version is the boot-client build version, shown in the corner.
	Version string
	// Choice is set when Handle returns ActionDone.
	Choice FailureChoice

	// layout records the rectangles of the last Draw's text blocks (in draw
	// order), for tests that check nothing runs off-screen or overlaps.
	layout []image.Rectangle
}

// NewFailureScreen builds the failure view.
func NewFailureScreen(info FailureInfo) *FailureScreen {
	return &FailureScreen{Info: info}
}

// failureActions is the keyboard help on the failure screen's action bar.
var failureActions = []struct{ key, label string }{
	{"R", "Retry"},
	{"Enter", "Restart machine"},
	{"Esc", "Diagnostic shell"},
}

func (s *FailureScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	fillRect(img, b, th.Bg)
	s.layout = s.layout[:0]
	in := s.Info

	right2 := ""
	if in.Elapsed > 0 {
		right2 = "Failed after " + formatElapsed(in.Elapsed)
	}
	code := ""
	if in.Code != "" {
		code = "Error " + in.Code
	}
	top := drawHeader(img, th, b, in.Identity, code, right2, th.Danger)
	// A red rule under the header marks the whole screen as an error state
	// at a glance, even from across the room.
	fillRect(img, image.Rect(b.Min.X, top, b.Max.X, top+6), th.Danger)
	top += 6

	// Content column: left-aligned text reads better than centred for a
	// multi-line message; capped so lines stay short on wide panels.
	colW := minInt(900, b.Dx()-80)
	x := b.Min.X + (b.Dx()-colW)/2

	// Action bar pinned to the bottom so the technician always sees what to
	// do next, however long the message is.
	barH := lineHeight(th.Body) + 28
	barTop := b.Max.Y - barH - 34
	limit := barTop - 12 // content must end above this

	title := in.Title
	if title == "" {
		title = "Deployment failed"
	}
	title = ellipsize(th.Title, title, colW)
	y := top + 22 + th.Title.Metrics().Ascent.Ceil()
	drawText(img, th.Title, th.Danger, x, y, title)
	s.note(x, y, th.Title, title)

	y += 14
	msg := in.Message
	if msg == "" {
		msg = "Something went wrong while preparing this machine."
	}
	for _, l := range wrapText(th.Body, msg, colW, 4) {
		y += lineHeight(th.Body) + 2
		drawText(img, th.Body, th.Text, x, y, l)
		s.note(x, y, th.Body, l)
	}

	// Last output, in a panel, only as many lines as fit above the hints.
	small := lineHeight(th.Small)
	mono := lineHeight(th.Mono)
	hintsH := 2*small + 22 // logs line + gap
	if len(in.Output) > 0 {
		avail := limit - hintsH - (y + 18 + small + 16)
		n := minInt(len(in.Output), avail/(mono+2))
		if n > 0 {
			lines := in.Output[len(in.Output)-n:]
			y += 18 + small
			drawText(img, th.Small, th.Muted, x, y, "Last output from the failing command:")
			s.note(x, y, th.Small, "Last output from the failing command:")
			y += 8
			box := image.Rect(x, y, x+colW, y+n*(mono+2)+14)
			fillRoundRect(img, box, 6, th.Panel)
			ly := y + 6 + th.Mono.Metrics().Ascent.Ceil()
			for _, l := range lines {
				t := ellipsize(th.Mono, l, colW-24)
				drawText(img, th.Mono, th.Text, x+12, ly, t)
				ly += mono + 2
			}
			s.layout = append(s.layout, box)
			y = box.Max.Y
		}
	}

	// Where the logs went.
	y += 18 + small
	logs := "Logs sent to the server: see Machine › Deployment in the AutoDeploy portal."
	if !in.LogsShipped {
		logs = "Couldn't send the logs to the server (is the network up?). Retry to try again."
	}
	logs = ellipsize(th.Small, logs, colW)
	drawText(img, th.Small, th.Muted, x, y, logs)
	s.note(x, y, th.Small, logs)

	// Action bar: key caps + labels.
	bar := image.Rect(x, barTop, x+colW, barTop+barH)
	fillRoundRect(img, bar, 10, th.Panel)
	ax := bar.Min.X + 16
	base := bar.Min.Y + (barH+th.Body.Metrics().Ascent.Ceil())/2 - 2
	for _, a := range failureActions {
		kw := textWidth(th.Small, a.key) + 18
		lw := textWidth(th.Body, a.label)
		if ax+kw+8+lw > bar.Max.X-12 {
			break // narrow panel: drop what doesn't fit rather than overflow
		}
		keycap := image.Rect(ax, bar.Min.Y+12, ax+kw, bar.Max.Y-12)
		fillRoundRect(img, keycap, 6, th.Primary)
		drawText(img, th.Small, th.OnPrimary, ax+9, base-1, a.key)
		drawText(img, th.Body, th.Text, keycap.Max.X+8, base, a.label)
		ax = keycap.Max.X + 8 + lw + 24
	}
	s.layout = append(s.layout, bar)
	// Version below the action bar (drawVersion's usual baseline would sit
	// inside the bar).
	if s.Version != "" {
		label := "boot " + s.Version
		drawText(img, th.Small, th.Muted, b.Max.X-textWidth(th.Small, label)-20, b.Max.Y-10, label)
	}
}

// note records the rectangle of a text line drawn at baseline (x,y).
func (s *FailureScreen) note(x, y int, face font.Face, text string) {
	m := face.Metrics()
	s.layout = append(s.layout, image.Rect(x, y-m.Ascent.Ceil(), x+textWidth(face, text), y+m.Descent.Ceil()))
}

// Handle maps R to retry, Enter to restart and Esc to the shell; every
// other input is ignored so a stray key can't dismiss the explanation.
func (s *FailureScreen) Handle(ev input.Event) Action {
	switch {
	case ev.Type == input.Rune && (ev.Rune == 'r' || ev.Rune == 'R'):
		s.Choice = FailureRetry
	case ev.Type == input.KeyPress && ev.Key == input.KeyEnter:
		s.Choice = FailureReboot
	case ev.Type == input.KeyPress && ev.Key == input.KeyEscape:
		s.Choice = FailureShell
	default:
		return ActionNone
	}
	return ActionDone
}

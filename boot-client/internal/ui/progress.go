package ui

import (
	"image"
	"sync"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// ProgressScreen shows deploy progress: a stage label, a step detail line,
// the current artifact (file) being fetched, and a percentage bar. It is
// updated from the deploy goroutine via Set* (concurrency-safe) and
// repainted by the App loop on a ticker, so it has no interactive Handle
// behaviour (input is ignored during deploy).
type ProgressScreen struct {
	Title string
	// Version is the boot-client build version, shown in the corner. Set
	// once before the screen is run; not mutated during the deploy.
	Version string

	mu      sync.Mutex
	stage   string
	detail  string
	file    string
	percent int // 0..100; <0 = indeterminate
}

// NewProgressScreen builds a progress view.
func NewProgressScreen(title string) *ProgressScreen {
	return &ProgressScreen{Title: title, percent: -1}
}

// Set updates the stage label, detail line, and percent (negative =
// indeterminate). Safe to call from the deploy goroutine.
func (p *ProgressScreen) Set(stage, detail string, percent int) {
	p.mu.Lock()
	p.stage, p.detail, p.percent = stage, detail, percent
	p.mu.Unlock()
}

// SetFile updates just the current-artifact line shown beneath the detail
// (e.g. the media file being copied), without disturbing the stage, detail
// or percent. Safe to call frequently from the deploy goroutine.
//
// Pass a bare filename or a media-relative path -- NEVER a URL. The progress
// screen is on display in the open during imaging, so it must not reveal the
// server address.
func (p *ProgressScreen) SetFile(file string) {
	p.mu.Lock()
	p.file = file
	p.mu.Unlock()
}

func (p *ProgressScreen) snapshot() (string, string, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stage, p.detail, p.file, p.percent
}

func (p *ProgressScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	stage, detail, file, pct := p.snapshot()
	fillRect(img, b, th.Bg)
	cx := b.Dx() / 2
	drawCentered(img, th.Title, th.Text, cx, b.Min.Y+b.Dy()/2-80, p.Title)
	if stage != "" {
		drawCentered(img, th.Body, th.Text, cx, b.Min.Y+b.Dy()/2-30, stage)
	}

	// Bar.
	barW := minInt(680, b.Dx()-160)
	barH := 18
	bx := cx - barW/2
	by := b.Min.Y + b.Dy()/2
	track := image.Rect(bx, by, bx+barW, by+barH)
	fillRoundRect(img, track, barH/2, th.Panel)
	if pct >= 0 {
		fw := barW * clampInt(pct, 0, 100) / 100
		if fw > 0 {
			fillRoundRect(img, image.Rect(bx, by, bx+fw, by+barH), barH/2, th.Primary)
		}
		drawCentered(img, th.Small, th.Muted, cx, by+barH+34, itoa(clampInt(pct, 0, 100))+"%")
	} else {
		// Indeterminate: a primary segment near the start.
		seg := barW / 4
		fillRoundRect(img, image.Rect(bx, by, bx+seg, by+barH), barH/2, th.Primary)
		drawCentered(img, th.Small, th.Muted, cx, by+barH+34, "Working…")
	}
	if detail != "" {
		drawCentered(img, th.Small, th.Muted, cx, by+barH+62, detail)
	}
	if file != "" {
		drawCentered(img, th.Small, th.Muted, cx, by+barH+88, file)
	}
	drawCentered(img, th.Small, th.Muted, cx, b.Max.Y-40, "Do not power off this machine")
	drawVersion(img, th, b, p.Version)
}

// Handle ignores input -- the deploy is not interruptible from the UI.
func (p *ProgressScreen) Handle(ev input.Event) Action { return ActionNone }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

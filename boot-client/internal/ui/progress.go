package ui

import (
	"image"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// ProgressScreen shows deploy progress: an identity header (machine,
// serial, IP, server, "Step N of M", elapsed time), a stage label, a step
// detail line, the current artifact (file) being fetched, and a
// percentage bar. It is updated from the deploy goroutine via Set*
// (concurrency-safe) and repainted by the App loop on a ticker, so it has
// no interactive Handle behaviour (input is ignored during deploy).
type ProgressScreen struct {
	Title string
	// Version is the boot-client build version, shown in the corner. Set
	// once before the screen is run; not mutated during the deploy.
	Version string
	// Identity is shown in the header band. Set once before the screen is
	// run; not mutated during the deploy.
	Identity Identity
	// Started is when the deploy began; the header shows the time since.
	// Zero hides the elapsed timer.
	Started time.Time
	// Now is the clock used for the elapsed timer (nil = time.Now). Tests
	// pin it so rendered screens are deterministic.
	Now func() time.Time

	mu      sync.Mutex
	stage   string
	detail  string
	file    string
	percent int // 0..100; <0 = indeterminate
	step    int // 1-based index of the current stage; 0 = unknown
	steps   int // total stages; 0 = unknown
}

// NewProgressScreen builds a progress view whose elapsed timer starts now.
func NewProgressScreen(title string) *ProgressScreen {
	return &ProgressScreen{Title: title, percent: -1, Started: time.Now()}
}

// Set updates the stage label, detail line, and percent (negative =
// indeterminate). Safe to call from the deploy goroutine.
func (p *ProgressScreen) Set(stage, detail string, percent int) {
	p.mu.Lock()
	p.stage, p.detail, p.percent = stage, detail, percent
	p.mu.Unlock()
}

// SetStep updates the "Step N of M" counter in the header (n or total
// <= 0 hides it). Safe to call from the deploy goroutine.
func (p *ProgressScreen) SetStep(n, total int) {
	p.mu.Lock()
	p.step, p.steps = n, total
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

func (p *ProgressScreen) stepSnapshot() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.step, p.steps
}

// elapsed is the time since Started, or "" when no start is set.
func (p *ProgressScreen) elapsed() string {
	if p.Started.IsZero() {
		return ""
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return "Elapsed " + formatElapsed(now().Sub(p.Started))
}

func (p *ProgressScreen) Draw(img *image.RGBA, th *Theme, b image.Rectangle) {
	stage, detail, file, pct := p.snapshot()
	step, steps := p.stepSnapshot()
	fillRect(img, b, th.Bg)

	stepLabel := ""
	if step > 0 && steps > 0 {
		stepLabel = "Step " + itoa(step) + " of " + itoa(steps)
	}
	top := drawHeader(img, th, b, p.Identity, stepLabel, p.elapsed(), th.Text)

	// Centre the content in the space below the header so it never runs
	// into the band on a short (600px) panel.
	cx := b.Min.X + b.Dx()/2
	cy := top + (b.Max.Y-top)/2 - 20
	textW := b.Dx() - 80
	drawCentered(img, th.Title, th.Text, cx, cy-80, ellipsize(th.Title, p.Title, textW))
	if stage != "" {
		drawCentered(img, th.Body, th.Text, cx, cy-30, ellipsize(th.Body, stage, textW))
	}

	// Bar.
	barW := minInt(680, b.Dx()-160)
	barH := 18
	bx := cx - barW/2
	by := cy
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
		drawCentered(img, th.Small, th.Muted, cx, by+barH+62, ellipsize(th.Small, detail, textW))
	}
	if file != "" {
		drawCentered(img, th.Small, th.Muted, cx, by+barH+88, ellipsize(th.Small, file, textW))
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

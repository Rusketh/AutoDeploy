//go:build linux

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/boot-client/internal/fb"
	"github.com/rusketh/autodeploy/boot-client/internal/input"
	"github.com/rusketh/autodeploy/boot-client/internal/ui"
)

// guiSession holds an initialised graphical UI (framebuffer + input + app).
// It is created by startGUI; a nil *guiSession means the GUI is
// unavailable and callers must fall back to the text console.
type guiSession struct {
	fb     *fb.Framebuffer
	reader *input.Reader
	app    *ui.App
	theme  *ui.Theme
	log    *slog.Logger
}

// startGUI tries to bring up the framebuffer GUI. Returns nil (no error
// surfaced to the caller beyond a log line) when the framebuffer or input
// can't be opened, so the caller falls back to the console. brand drives
// the theme colour.
func startGUI(log *slog.Logger, brand brandResp) *guiSession {
	surface, err := fb.Open("")
	if err != nil {
		log.Info("gui.unavailable", slog.String("reason", err.Error()))
		return nil
	}
	w, h := surface.Size()
	// Scale fonts up a touch on very large panels.
	scale := 1.0
	if h >= 1440 {
		scale = 1.4
	} else if h >= 1080 {
		scale = 1.15
	}
	theme, err := ui.NewTheme(brand.PrimaryColor, scale)
	if err != nil {
		log.Warn("gui.theme", slog.String("error", err.Error()))
		_ = surface.Close()
		return nil
	}
	reader, err := input.NewReader(w, h)
	if err != nil {
		log.Info("gui.noinput", slog.String("reason", err.Error()))
		_ = surface.Close()
		return nil
	}
	return &guiSession{
		fb:     surface,
		reader: reader,
		app:    ui.NewApp(surface, theme),
		theme:  theme,
		log:    log,
	}
}

func (g *guiSession) close() {
	if g == nil {
		return
	}
	if g.reader != nil {
		g.reader.Close()
	}
	if g.fb != nil {
		_ = g.fb.Close()
	}
}

// menu shows the graphical menu and returns the chosen item index, or -1 if
// cancelled.
func (g *guiSession) menu(title, subtitle string, items []ui.MenuItem) int {
	scr := ui.NewMenuScreen(title, subtitle, items)
	if g.app.Run(scr, g.reader.Events()) == ui.ActionDone {
		return scr.Chosen
	}
	return -1
}

// pin shows the PIN screen and returns the entered PIN, or "" if cancelled.
// message is an optional status/error line.
func (g *guiSession) pin(title, message string) (string, bool) {
	scr := ui.NewPINScreen(title)
	scr.Message = message
	if g.app.Run(scr, g.reader.Events()) == ui.ActionDone {
		return scr.Entered, true
	}
	return "", false
}

// progress returns a ProgressScreen plus a stop func. It paints the screen
// on a ticker in the background so deploy code can call Set(...) freely.
func (g *guiSession) progress(ctx context.Context, title string) (*ui.ProgressScreen, func()) {
	scr := ui.NewProgressScreen(title)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.app.Render(scr)
			}
		}
	}()
	// Initial paint.
	g.app.Render(scr)
	return scr, cancel
}

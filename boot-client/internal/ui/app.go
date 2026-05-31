package ui

import (
	"image"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// Surface is what the UI draws onto and presents. The framebuffer backend
// implements it; tests use a memSurface over a plain image.
type Surface interface {
	Image() *image.RGBA
	Size() (w, h int)
	Flush()
}

// Action is what a screen asks the App to do after handling input.
type Action int

const (
	ActionNone   Action = iota
	ActionRedraw        // state changed; repaint
	ActionDone          // this screen is finished (see screen-specific result)
	ActionCancel        // user backed out / cancelled
)

// Screen is one full-window view (menu, PIN, progress).
type Screen interface {
	// Draw paints the whole screen into img using theme; bounds is the
	// drawable area (full framebuffer).
	Draw(img *image.RGBA, th *Theme, bounds image.Rectangle)
	// Handle processes one input event and returns the resulting Action.
	Handle(ev input.Event) Action
}

// App owns the surface, theme, and pointer state, and runs a screen until
// it returns ActionDone/ActionCancel.
type App struct {
	surf       Surface
	theme      *Theme
	curX       int
	curY       int
	hasPointer bool
}

// NewApp builds an App over surf with the given theme.
func NewApp(surf Surface, theme *Theme) *App {
	w, h := surf.Size()
	return &App{surf: surf, theme: theme, curX: w / 2, curY: h / 2}
}

// Render paints scr once (used between event-driven redraws, e.g. progress
// updates).
func (a *App) Render(scr Screen) {
	img := a.surf.Image()
	w, h := a.surf.Size()
	bounds := image.Rect(0, 0, w, h)
	scr.Draw(img, a.theme, bounds)
	if a.hasPointer {
		drawCursor(img, a.curX, a.curY, a.theme.Text)
	}
	a.surf.Flush()
}

// Run paints scr and feeds it events until it finishes; returns the final
// Action (ActionDone or ActionCancel). Pointer movement is tracked for the
// cursor and forwarded to the screen.
func (a *App) Run(scr Screen, events <-chan input.Event) Action {
	a.Render(scr)
	for ev := range events {
		if ev.Type == input.PointerMove || ev.Type == input.PointerDown || ev.Type == input.PointerUp {
			a.curX, a.curY = ev.X, ev.Y
			a.hasPointer = true
		}
		act := scr.Handle(ev)
		switch act {
		case ActionDone, ActionCancel:
			return act
		case ActionRedraw:
			a.Render(scr)
		default:
			// Still repaint on pointer moves so the cursor tracks.
			if ev.Type == input.PointerMove {
				a.Render(scr)
			}
		}
	}
	return ActionCancel
}

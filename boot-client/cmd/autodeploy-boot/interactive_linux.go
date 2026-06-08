//go:build linux

package main

import (
	"context"
	"log/slog"

	"github.com/rusketh/autodeploy/boot-client/internal/httpc"
	"github.com/rusketh/autodeploy/boot-client/internal/logging"
	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
	"github.com/rusketh/autodeploy/boot-client/internal/ui"
)

// startInteractive runs the full graphical flow: PIN gate (if the server
// requires one) -> menu -> deploy with a live progress screen. It returns
// true if the GUI handled the session (deployed, or the operator
// cancelled), and false if the framebuffer/input could not be initialised
// so the caller falls back to the text console.
func startInteractive(ctx context.Context, log *slog.Logger, f bootFlags, c *httpc.Client, id smbios.Identity, resp menuResponse, brand brandResp, shipper *logging.Shipper) bool {
	g := startGUI(log, brand)
	if g == nil {
		return false // no framebuffer/input -> caller uses the console
	}
	defer g.close()

	// PIN gate (graphical). The server decides whether a PIN is required;
	// guiAccessPIN returns false only on cancel/lockout.
	if !guiAccessPIN(ctx, log, g, c, id) {
		log.Info("gui.cancel", slog.String("reason", "pin gate not passed"))
		return true // handled: operator cancelled / locked out
	}

	title := brandTitle(brand)
	items := make([]ui.MenuItem, 0, len(resp.Items)+1)
	// Re-image first, if offered.
	reimageIdx := -1
	if resp.Reimage != nil {
		items = append(items, ui.MenuItem{Title: resp.Reimage.Name, Desc: "Re-image this machine"})
		reimageIdx = 0
	}
	base := len(items)
	for _, it := range resp.Items {
		items = append(items, ui.MenuItem{Title: it.Name, Desc: it.Description})
	}
	choice, showProgress := g.menu(title, "Select a configuration to deploy", items)
	if choice < 0 {
		log.Info("gui.cancel", slog.String("reason", "menu cancelled"))
		return true
	}

	var imageID int64
	if choice == reimageIdx && resp.Reimage != nil {
		imageID = resp.Reimage.ImageID
	} else {
		imageID = resp.Items[choice-base].ImageID
	}

	// Operator unticked "Show imaging progress": hand the screen back to the
	// text console for the deploy so the kernel and boot-client logs are
	// visible on the machine. This is the way to debug a deploy that stalls
	// before it can ship any logs to the portal -- e.g. a USB NIC that wedges
	// the payload download. Tear the GUI down (fb.Close restores the text
	// console), route stage updates to the log so progress still scrolls past,
	// then deploy with everything going to the console.
	if !showProgress {
		log.Info("gui.progress.disabled",
			slog.String("reason", "operator unticked show-progress; deploying on the console for debugging"),
			slog.Int64("image_id", imageID))
		g.close()
		setProgressSink(&consoleProgressSink{log: log})
		defer setProgressSink(nil)
		runDeploy(log, f, id, imageID, shipper)
		return true
	}

	// Deploy with a live progress screen. Install the GUI progress sink so
	// runDeploy's stage reports paint the bar; clear it afterwards.
	prog, stop := g.progress(ctx, title)
	setProgressSink(&guiProgressSink{scr: prog})
	defer func() {
		stop()
		setProgressSink(nil)
	}()
	runDeploy(log, f, id, imageID, shipper)
	return true
}

// guiAccessPIN runs the access-PIN gate graphically. It mirrors the console
// runAccessPIN contract: an initial empty attempt lets the server say "no
// PIN required"; otherwise up to three masked attempts. Returns true when
// access is granted.
func guiAccessPIN(ctx context.Context, log *slog.Logger, g *guiSession, c *httpc.Client, id smbios.Identity) bool {
	if granted, _ := submitPIN(ctx, c, id, ""); granted {
		return true // server has no PIN configured
	}
	msg := ""
	for attempt := 1; attempt <= 3; attempt++ {
		pin, ok := g.pin("Enter access PIN", msg)
		if !ok || pin == "" {
			return false
		}
		granted, locked := submitPIN(ctx, c, id, pin)
		if granted {
			return true
		}
		if locked {
			log.Warn("gui.pin.lockout")
			return false
		}
		msg = "Incorrect PIN, try again"
	}
	return false
}

// guiProgressSink adapts a ui.ProgressScreen to the progressSink interface
// runDeploy reports to.
type guiProgressSink struct{ scr *ui.ProgressScreen }

func (s *guiProgressSink) Stage(stage, detail string, percent int) {
	s.scr.Set(stage, detail, percent)
}

func (s *guiProgressSink) File(name string) {
	s.scr.SetFile(name)
}

// consoleProgressSink logs deploy stage and current-file transitions to the
// boot client's logger (which writes to the console) when the operator turned
// the graphical progress screen OFF. It logs only on change -- never on the
// per-chunk percentage updates that share a stage+detail -- so the console
// shows readable progress and stall messages without a flood, interleaved with
// the slog diagnostics (NIC driver, link speed, retries) that make a stuck
// deploy debuggable.
type consoleProgressSink struct {
	log        *slog.Logger
	lastStage  string
	lastDetail string
	lastFile   string
}

func (s *consoleProgressSink) Stage(stage, detail string, percent int) {
	if stage == s.lastStage && detail == s.lastDetail {
		return
	}
	s.lastStage, s.lastDetail = stage, detail
	s.log.Info("deploy.progress",
		slog.String("stage", stage),
		slog.String("detail", detail),
		slog.Int("percent", percent))
}

func (s *consoleProgressSink) File(name string) {
	if name == "" || name == s.lastFile {
		return
	}
	s.lastFile = name
	s.log.Info("deploy.progress.file", slog.String("file", name))
}

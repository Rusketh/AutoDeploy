package ui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/boot-client/internal/input"
)

// Panel sizes the boot UI must stay legible at: the common efifb/simpledrm
// defaults (800x600 is what many firmwares and Hyper-V Gen2 hand over),
// plus a 1080p panel where the theme scales up.
var shotSizes = []image.Point{{800, 600}, {1024, 768}, {1920, 1080}}

var shotIdentity = Identity{
	Name:   "LAB-PC-0142",
	Model:  "Dell Inc. OptiPlex 7090",
	Serial: "7XK9Q53",
	IP:     "10.20.30.142",
}

func sampleProgress() *ProgressScreen {
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	p := NewProgressScreen("Contoso IT Deployment")
	p.Version = "v1.8.0"
	p.Identity = shotIdentity
	p.Started = start
	p.Now = func() time.Time { return start.Add(4*time.Minute + 12*time.Second) }
	p.Set("Downloading image", "Copying install media to disk (2.1 / 5.8 GB)", 36)
	p.SetFile("sources/install.wim")
	p.SetStep(4, 6)
	return p
}

func sampleFailure() *FailureScreen {
	f := NewFailureScreen(FailureInfo{
		Message:  "Couldn't prepare the disk (partitioning failed): partition /dev/nvme0n1: sgdisk: exit status 4",
		Code:     "PARTITION",
		Identity: shotIdentity,
		Output: []string{
			"Caution: invalid main GPT header, but valid backup; regenerating main header",
			"from backup!",
			"Warning: Partition table header claims that the size of partition table",
			"entries is 0 bytes, but this program supports only 128-byte entries.",
			"Could not create partition 1 from 2048 to 4196351",
			"Error encountered; not saving changes.",
		},
		LogsShipped: true,
		Elapsed:     2*time.Minute + 5*time.Second,
	})
	f.Version = "v1.8.0"
	return f
}

// renderAt draws scr at size with the theme scale the GUI would pick.
func renderAt(t *testing.T, scr Screen, size image.Point) *image.RGBA {
	t.Helper()
	th, err := NewTheme("#0b65c2", ScaleForHeight(size.Y))
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	scr.Draw(img, th, img.Bounds())
	return img
}

// saveShot writes img as a PNG under $AUTODEPLOY_UI_SHOTS (when set), so
// the screens can be eyeballed without a framebuffer:
//
//	AUTODEPLOY_UI_SHOTS=/tmp/shots go test ./internal/ui -run Shots
func saveShot(t *testing.T, img image.Image, name string) {
	t.Helper()
	dir := os.Getenv("AUTODEPLOY_UI_SHOTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestProgressShots(t *testing.T) {
	for _, sz := range shotSizes {
		img := renderAt(t, sampleProgress(), sz)
		saveShot(t, img, fmt.Sprintf("progress_%dx%d.png", sz.X, sz.Y))
	}
	// Unknown identity / step: still renders.
	p := NewProgressScreen("Deploying")
	saveShot(t, renderAt(t, p, image.Pt(800, 600)), "progress_minimal_800x600.png")
}

// TestFailureShotsLayout renders the failure screen at every supported
// size and checks the layout: every text block on-screen, nothing
// overlapping the pinned action bar, and blocks in top-to-bottom order.
func TestFailureShotsLayout(t *testing.T) {
	variants := map[string]func() *FailureScreen{
		"failure": sampleFailure,
		"failure_nolog": func() *FailureScreen {
			f := sampleFailure()
			f.Info.Output = nil
			f.Info.LogsShipped = false
			f.Info.Code = "MENU-FETCH"
			f.Info.Title = "Couldn't start the deployment"
			f.Info.Message = "Couldn't reach the AutoDeploy server to get the deployment menu: dial tcp 10.0.0.1:8443: connect: no route to host"
			f.Info.Identity = Identity{Model: "QEMU Standard PC (Q35 + ICH9, 2009)", Serial: "", IP: ""}
			return f
		},
	}
	for name, mk := range variants {
		for _, sz := range shotSizes {
			f := mk()
			img := renderAt(t, f, sz)
			saveShot(t, img, fmt.Sprintf("%s_%dx%d.png", name, sz.X, sz.Y))
			bounds := img.Bounds()
			if len(f.layout) < 4 {
				t.Fatalf("%s %v: layout not recorded: %v", name, sz, f.layout)
			}
			bar := f.layout[len(f.layout)-1]
			prev := 0
			for _, r := range f.layout[:len(f.layout)-1] {
				if !r.In(bounds) {
					t.Errorf("%s %v: block %v off-screen", name, sz, r)
				}
				if r.Overlaps(bar) {
					t.Errorf("%s %v: block %v overlaps the action bar %v", name, sz, r, bar)
				}
				if r.Min.Y < prev {
					t.Errorf("%s %v: block %v drawn above the previous one (y=%d)", name, sz, r, prev)
				}
				prev = r.Max.Y
			}
			if !bar.In(bounds) {
				t.Errorf("%s %v: action bar %v off-screen", name, sz, bar)
			}
		}
	}
}

func TestFailureKeys(t *testing.T) {
	cases := []struct {
		ev   input.Event
		want FailureChoice
	}{
		{input.Event{Type: input.Rune, Rune: 'r'}, FailureRetry},
		{input.Event{Type: input.Rune, Rune: 'R'}, FailureRetry},
		{input.Event{Type: input.KeyPress, Key: input.KeyEnter}, FailureReboot},
		{input.Event{Type: input.KeyPress, Key: input.KeyEscape}, FailureShell},
	}
	for _, c := range cases {
		f := NewFailureScreen(FailureInfo{})
		if a := f.Handle(c.ev); a != ActionDone || f.Choice != c.want {
			t.Errorf("%+v: action=%v choice=%v, want Done/%v", c.ev, a, f.Choice, c.want)
		}
	}
	f := NewFailureScreen(FailureInfo{})
	for _, ev := range []input.Event{
		{Type: input.Rune, Rune: 'x'},
		{Type: input.KeyPress, Key: input.KeyDown},
		{Type: input.PointerDown, X: 1, Y: 1},
	} {
		if a := f.Handle(ev); a != ActionNone || f.Choice != FailureNone {
			t.Errorf("stray %+v should be ignored, got %v/%v", ev, a, f.Choice)
		}
	}
}

func TestHeaderHelpers(t *testing.T) {
	th := testTheme(t)
	long := "Couldn't prepare the disk (partitioning failed): zap /dev/sda: exit status 2 and a lot more detail"
	for _, l := range wrapText(th.Body, long, 300, 3) {
		if w := textWidth(th.Body, l); w > 300 {
			t.Errorf("wrapped line %q is %dpx > 300", l, w)
		}
	}
	if got := wrapText(th.Body, long, 120, 2); len(got) != 2 {
		t.Errorf("maxLines not honoured: %q", got)
	}
	if e := ellipsize(th.Small, long, 100); textWidth(th.Small, e) > 100 || e == long {
		t.Errorf("ellipsize = %q", e)
	}
	if ellipsize(th.Small, "short", 500) != "short" {
		t.Error("short text should be unchanged")
	}
	for d, want := range map[time.Duration]string{
		0: "0:00", 65 * time.Second: "1:05", 3723 * time.Second: "1:02:03",
	} {
		if got := formatElapsed(d); got != want {
			t.Errorf("formatElapsed(%v) = %q, want %q", d, got, want)
		}
	}
	id := Identity{Serial: "S1", IP: "1.2.3.4"}
	if id.primary() != "This machine" || id.details() != "S/N S1  ·  IP 1.2.3.4" ||
		(Identity{Name: "PC1", Model: "M"}).details() != "M" {
		t.Errorf("identity lines = %q / %q", id.primary(), id.details())
	}
}

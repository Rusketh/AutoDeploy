// Package input reads Linux evdev devices (/dev/input/event*) and turns
// them into a neutral stream of UI events: key presses, pointer motion,
// and clicks. Pure Go (CGO-free) via golang.org/x/sys/unix. It aggregates
// every input device it can open, so a machine's keyboard and mouse/
// touchpad (USB HID, PS/2, Hyper-V synthetic, VMware) all feed one stream.
package input

// EventType classifies a UI event.
type EventType int

const (
	// KeyPress is a key-down for a key we map to a UI action.
	KeyPress EventType = iota
	// Rune is a printable character typed (for PIN / text entry).
	Rune
	// PointerMove updates the absolute cursor position (X,Y).
	PointerMove
	// PointerDown / PointerUp are primary-button transitions at X,Y.
	PointerDown
	PointerUp
)

// Key is a logical key the UI reacts to (navigation + editing), decoded
// from evdev key codes so the UI never sees raw scancodes.
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyEnter
	KeyEscape
	KeyBackspace
	KeyTab
)

// Event is one neutral UI input event.
type Event struct {
	Type EventType
	Key  Key
	Rune rune
	X, Y int // for pointer events (absolute, clamped to screen by the reader)
}

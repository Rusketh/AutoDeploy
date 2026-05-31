//go:build linux

package input

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
)

// inputEventSize is sizeof(struct input_event) on 64-bit Linux:
// struct timeval (16) + type(2) + code(2) + value(4) = 24 bytes.
const inputEventSize = 24

// rawEvent is one decoded input_event (timeval discarded).
type rawEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

// decodeRaw parses one 24-byte input_event record. ok is false if the
// buffer is too short.
func decodeRaw(b []byte) (rawEvent, bool) {
	if len(b) < inputEventSize {
		return rawEvent{}, false
	}
	return rawEvent{
		Type:  binary.LittleEndian.Uint16(b[16:18]),
		Code:  binary.LittleEndian.Uint16(b[18:20]),
		Value: int32(binary.LittleEndian.Uint32(b[20:24])),
	}, true
}

// Reader aggregates every openable /dev/input/event* device and emits
// neutral Events on Events(). Pointer state (x,y) is tracked here so
// relative mice and absolute pointers both produce PointerMove with
// screen coordinates.
type Reader struct {
	w, h    int
	x, y    int
	shift   bool
	out     chan Event
	files   []*os.File
	once    sync.Once
	closed  chan struct{}
}

// NewReader opens all input devices and starts reading. w,h bound the
// pointer position. Returns an error only if no device could be opened.
func NewReader(w, h int) (*Reader, error) {
	r := &Reader{w: w, h: h, x: w / 2, y: h / 2, out: make(chan Event, 64), closed: make(chan struct{})}
	matches, _ := filepath.Glob("/dev/input/event*")
	for _, p := range matches {
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		r.files = append(r.files, f)
		go r.readLoop(f)
	}
	if len(r.files) == 0 {
		return nil, os.ErrNotExist
	}
	return r, nil
}

// Events returns the neutral event stream.
func (r *Reader) Events() <-chan Event { return r.out }

// Close stops reading and closes all devices.
func (r *Reader) Close() {
	r.once.Do(func() {
		close(r.closed)
		for _, f := range r.files {
			_ = f.Close()
		}
	})
}

func (r *Reader) readLoop(f *os.File) {
	buf := make([]byte, inputEventSize*16)
	for {
		select {
		case <-r.closed:
			return
		default:
		}
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for off := 0; off+inputEventSize <= n; off += inputEventSize {
			ev, ok := decodeRaw(buf[off : off+inputEventSize])
			if !ok {
				continue
			}
			r.handle(ev)
		}
	}
}

// handle translates one raw event into zero or more neutral events.
func (r *Reader) handle(ev rawEvent) {
	switch ev.Type {
	case evKey:
		switch int(ev.Code) {
		case keyLeftShift, keyRightShift:
			r.shift = ev.Value != 0
			return
		case btnLeft, btnTouch:
			if ev.Value != 0 {
				r.emit(Event{Type: PointerDown, X: r.x, Y: r.y})
			} else {
				r.emit(Event{Type: PointerUp, X: r.x, Y: r.y})
			}
			return
		}
		if ev.Value == 0 { // key up: ignore for nav/typing (we act on press/repeat)
			return
		}
		if k := navKey(ev.Code); k != KeyNone {
			r.emit(Event{Type: KeyPress, Key: k})
			return
		}
		if ru := asciiForKey(ev.Code, r.shift); ru != 0 {
			r.emit(Event{Type: Rune, Rune: ru})
		}
	case evRel:
		switch int(ev.Code) {
		case relX:
			r.movePointer(int(ev.Value), 0)
		case relY:
			r.movePointer(0, int(ev.Value))
		}
	case evAbs:
		// Absolute pointers report device units; we can't know the axis
		// range without EVIOCGABS, so treat the value as already in a
		// 0..32767 range (the common touchpad/abs-pointer convention) and
		// scale to the screen.
		switch int(ev.Code) {
		case absX:
			r.x = clamp(int(int64(ev.Value)*int64(r.w)/32767), 0, r.w-1)
			r.emit(Event{Type: PointerMove, X: r.x, Y: r.y})
		case absY:
			r.y = clamp(int(int64(ev.Value)*int64(r.h)/32767), 0, r.h-1)
			r.emit(Event{Type: PointerMove, X: r.x, Y: r.y})
		}
	}
}

func (r *Reader) movePointer(dx, dy int) {
	r.x = clamp(r.x+dx, 0, r.w-1)
	r.y = clamp(r.y+dy, 0, r.h-1)
	r.emit(Event{Type: PointerMove, X: r.x, Y: r.y})
}

func (r *Reader) emit(e Event) {
	select {
	case r.out <- e:
	case <-r.closed:
	default: // drop if the UI is briefly behind; input is high-frequency
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

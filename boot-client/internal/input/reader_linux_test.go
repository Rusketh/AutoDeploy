//go:build linux

package input

import (
	"encoding/binary"
	"testing"
)

func makeRaw(typ, code uint16, val int32) []byte {
	b := make([]byte, inputEventSize)
	binary.LittleEndian.PutUint16(b[16:18], typ)
	binary.LittleEndian.PutUint16(b[18:20], code)
	binary.LittleEndian.PutUint32(b[20:24], uint32(val))
	return b
}

func TestDecodeRaw(t *testing.T) {
	ev, ok := decodeRaw(makeRaw(evKey, keyEnter, 1))
	if !ok || ev.Type != evKey || ev.Code != keyEnter || ev.Value != 1 {
		t.Fatalf("decodeRaw = %+v ok=%v", ev, ok)
	}
	if _, ok := decodeRaw(make([]byte, 10)); ok {
		t.Error("short buffer should not decode")
	}
}

func TestReaderKeyboardNav(t *testing.T) {
	r := &Reader{w: 800, h: 600, x: 400, y: 300, out: make(chan Event, 16), closed: make(chan struct{})}
	// Down arrow press -> KeyDown.
	r.handle(rawEvent{Type: evKey, Code: keyDown, Value: 1})
	// Key up should be ignored.
	r.handle(rawEvent{Type: evKey, Code: keyDown, Value: 0})
	// Enter -> KeyEnter.
	r.handle(rawEvent{Type: evKey, Code: keyEnter, Value: 1})
	got := drain(r.out)
	if len(got) != 2 || got[0].Key != KeyDown || got[1].Key != KeyEnter {
		t.Fatalf("nav events = %+v", got)
	}
}

func TestReaderTypingWithShift(t *testing.T) {
	r := &Reader{w: 800, h: 600, out: make(chan Event, 16), closed: make(chan struct{})}
	r.handle(rawEvent{Type: evKey, Code: 2, Value: 1}) // '1'
	r.handle(rawEvent{Type: evKey, Code: keyLeftShift, Value: 1})
	r.handle(rawEvent{Type: evKey, Code: 2, Value: 1}) // shift+1 = '!'
	r.handle(rawEvent{Type: evKey, Code: keyLeftShift, Value: 0})
	r.handle(rawEvent{Type: evKey, Code: 30, Value: 1}) // 'a'
	got := drain(r.out)
	if len(got) != 3 || got[0].Rune != '1' || got[1].Rune != '!' || got[2].Rune != 'a' {
		t.Fatalf("typing events = %+v", got)
	}
}

func TestReaderRelativePointer(t *testing.T) {
	r := &Reader{w: 800, h: 600, x: 10, y: 10, out: make(chan Event, 16), closed: make(chan struct{})}
	r.handle(rawEvent{Type: evRel, Code: relX, Value: 5})
	r.handle(rawEvent{Type: evRel, Code: relY, Value: -3})
	got := drain(r.out)
	if len(got) != 2 || got[0].Type != PointerMove || got[0].X != 15 || got[1].Y != 7 {
		t.Fatalf("pointer events = %+v", got)
	}
	// Clamps at edges.
	r2 := &Reader{w: 800, h: 600, x: 0, y: 0, out: make(chan Event, 16), closed: make(chan struct{})}
	r2.handle(rawEvent{Type: evRel, Code: relX, Value: -100})
	g := drain(r2.out)
	if g[0].X != 0 {
		t.Errorf("clamp left = %d, want 0", g[0].X)
	}
}

func TestReaderClick(t *testing.T) {
	r := &Reader{w: 800, h: 600, x: 100, y: 200, out: make(chan Event, 16), closed: make(chan struct{})}
	r.handle(rawEvent{Type: evKey, Code: btnLeft, Value: 1})
	r.handle(rawEvent{Type: evKey, Code: btnLeft, Value: 0})
	got := drain(r.out)
	if len(got) != 2 || got[0].Type != PointerDown || got[0].X != 100 || got[1].Type != PointerUp {
		t.Fatalf("click events = %+v", got)
	}
}

func drain(ch chan Event) []Event {
	var out []Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

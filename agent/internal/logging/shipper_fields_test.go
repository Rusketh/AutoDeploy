package logging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestShipper(capacity int) (*slog.Logger, *Shipper) {
	sh := NewShipper(slog.NewJSONHandler(io.Discard, nil), capacity)
	return slog.New(sh), sh
}

func decodeFields(t *testing.T, ev LogEvent) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(ev.Fields), &m); err != nil {
		t.Fatalf("fields %q: %v", ev.Fields, err)
	}
	return m
}

type stringerT struct{ n int }

func (s stringerT) String() string { return fmt.Sprintf("thing-%d", s.n) }

type nilErr struct{ msg string }

func (e *nilErr) Error() string { return e.msg }

// TestShipperStringifiesErrors is the regression guard for errors being
// shipped as `{}`: an error value (plain, wrapped, or in a group) must
// arrive as its message; nil stays null; Stringers use String() while
// types with their own text encoding (net.IP) are left to it.
func TestShipperStringifiesErrors(t *testing.T) {
	log, sh := newTestShipper(16)
	base := errors.New("disk on fire")
	var typedNil *nilErr
	log.Error("x",
		slog.Any("error", base),
		slog.Any("wrapped", fmt.Errorf("partition: %w", base)),
		slog.Any("none", error(nil)),
		slog.Any("typed_nil", typedNil),
		slog.Any("thing", stringerT{7}),
		slog.Any("ip", net.ParseIP("10.0.0.5")),
		slog.Group("g", slog.Any("err", base), slog.Int("n", 2)),
	)
	evs := sh.Drain()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	f := decodeFields(t, evs[0])
	want := map[string]any{
		"error":     "disk on fire",
		"wrapped":   "partition: disk on fire",
		"thing":     "thing-7",
		"ip":        "10.0.0.5",
		"typed_nil": "<nil error>",
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("fields[%q] = %#v, want %#v (all: %s)", k, f[k], v, evs[0].Fields)
		}
	}
	if v, ok := f["none"]; !ok || v != nil {
		t.Errorf("nil error should ship as null, got %#v (present=%v)", v, ok)
	}
	g, ok := f["g"].(map[string]any)
	if !ok || g["err"] != "disk on fire" || g["n"] != float64(2) {
		t.Errorf("group = %#v", f["g"])
	}
}

// TestShipperDefaultActor: a logger-wide actor (attached once via With)
// lands on every event, and a per-call actor still overrides it.
func TestShipperDefaultActor(t *testing.T) {
	log, sh := newTestShipper(16)
	log = log.With(slog.String("actor", "uuid-1"), slog.String("component", "agent"))
	log.Info("a")
	log.Info("b", slog.String("actor", "operator"))
	log.With(slog.Int64("deployment_id", 42)).Info("c")
	evs := sh.Drain()
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	if evs[0].Actor != "uuid-1" || evs[0].Component != "agent" {
		t.Errorf("default actor/component not applied: %+v", evs[0])
	}
	if evs[1].Actor != "operator" {
		t.Errorf("per-call actor should override, got %q", evs[1].Actor)
	}
	if evs[2].Actor != "uuid-1" || decodeFields(t, evs[2])["deployment_id"] != float64(42) {
		t.Errorf("deployment_id/actor missing: %+v", evs[2])
	}
}

// TestShipperKeepsWarningsUnderInfoFlood: a full buffer evicts the oldest
// INFO first, so an early ERROR survives any amount of stdout noise, and
// the batch reports how many events were dropped.
func TestShipperKeepsWarningsUnderInfoFlood(t *testing.T) {
	log, sh := newTestShipper(4)
	log.Info("start")
	log.Error("step.fail")
	log.Warn("step.warn")
	for i := 0; i < 50; i++ {
		log.Info("steps.exec.stdout", slog.Int("i", i))
	}
	evs := sh.Drain()
	if len(evs) != 5 {
		t.Fatalf("want dropped marker + 4 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Action != "logs.buffer.dropped" || evs[0].Level != "WARN" {
		t.Fatalf("first event should be the dropped marker, got %+v", evs[0])
	}
	if d := decodeFields(t, evs[0])["dropped"]; d != float64(49) {
		t.Errorf("dropped = %v, want 49", d)
	}
	got := []string{evs[1].Action, evs[2].Action, evs[3].Action, evs[4].Action}
	want := []string{"step.fail", "step.warn", "steps.exec.stdout", "steps.exec.stdout"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// The newest INFO lines are the ones kept.
	if decodeFields(t, evs[4])["i"] != float64(49) || decodeFields(t, evs[3])["i"] != float64(48) {
		t.Errorf("expected the two newest stdout lines kept: %s / %s", evs[3].Fields, evs[4].Fields)
	}
	if again := sh.Drain(); again != nil {
		t.Errorf("drain should reset the dropped counter, got %+v", again)
	}
}

// TestShipperAllWarnEvictsOldest: with no INFO left to evict, the oldest
// event of any level goes.
func TestShipperAllWarnEvictsOldest(t *testing.T) {
	log, sh := newTestShipper(2)
	log.Warn("w1")
	log.Warn("w2")
	log.Error("e3")
	evs := sh.Drain()
	if len(evs) != 3 || evs[1].Action != "w2" || evs[2].Action != "e3" {
		t.Fatalf("got %+v", evs)
	}
}

// TestShipperShipsMachineHeaderAndRequeuesByPriority checks the ingest
// POST carries the machine UUID header, and that a failed ship requeues
// under the same WARN-preserving eviction.
func TestShipperShipsMachineHeaderAndRequeuesByPriority(t *testing.T) {
	fail := true
	var gotHeader string
	var gotEvents int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-AutoDeploy-Machine-UUID")
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Events []LogEvent `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotEvents = len(body.Events)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	log, sh := newTestShipper(3)
	sh.SetMachineUUID("uuid-9")
	log.Error("boom")
	log.Info("i1")
	log.Info("i2")
	if _, err := sh.Ship(context.Background(), srv.URL, false); err == nil {
		t.Fatal("expected ship failure")
	}
	if gotHeader != "uuid-9" {
		t.Errorf("machine header = %q", gotHeader)
	}
	// More INFO arrives while the server is down; the ERROR must survive.
	log.Info("i3")
	log.Info("i4")
	fail = false
	n, err := sh.Ship(context.Background(), srv.URL, false)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if n != 4 || gotEvents != 4 { // dropped marker + boom + i3 + i4
		t.Fatalf("shipped %d (server saw %d), want 4", n, gotEvents)
	}
}

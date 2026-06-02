package logging

import (
	"errors"
	"log/slog"
	"testing"
	"time"
)

// failingWriter always errors, standing in for os.Stdout under a
// console-less Windows service.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the handle is invalid") }

// TestShipperBuffersDespiteDeadSink is the regression guard for the
// "agent logs are bare" symptom: when the local sink (stdout under the
// SCM) fails on every write, records must STILL be queued for shipment
// to the server.
func TestShipperBuffersDespiteDeadSink(t *testing.T) {
	base := slog.NewJSONHandler(failingWriter{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	sh := NewShipper(base, 16)
	log := slog.New(sh)

	log.Info("agent.start", slog.String("server", "https://srv"))
	log.Info("self.fetch.ok")

	events := sh.Drain()
	if len(events) != 2 {
		t.Fatalf("want 2 buffered events despite dead sink, got %d", len(events))
	}
	if events[0].Action != "agent.start" || events[1].Action != "self.fetch.ok" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

// TestBestEffortWriterNeverErrors confirms a failing underlying writer
// is swallowed so it cannot abort an io.MultiWriter chain.
func TestBestEffortWriterNeverErrors(t *testing.T) {
	b := &bestEffortWriter{w: failingWriter{}}
	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("bestEffortWriter returned error: %v", err)
	}
	if n != 5 {
		t.Fatalf("bestEffortWriter reported short write: %d", n)
	}
}

// TestNewWithShipperResilient confirms the constructed logger still
// queues events for shipment when its primary writer is dead.
func TestNewWithShipperResilient(t *testing.T) {
	log, sh := NewWithShipper(failingWriter{}, "agent", 8)
	log.Info("checkin.ok", slog.Time("t", time.Now()))
	if got := len(sh.Drain()); got != 1 {
		t.Fatalf("want 1 buffered event, got %d", got)
	}
}

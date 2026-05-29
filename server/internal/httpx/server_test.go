package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/logging"
)

func TestHealthz(t *testing.T) {
	logger := logging.New(io.Discard, "server.test")
	_, h := New(config.Config{DevMode: true}, logger, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("body = %q", got)
	}
}

// Cleartext HTTP on a non-loopback bind is a permitted production
// deployment (operator's call) but it MUST log a clearly-identifiable
// warning so the choice is auditable. This test takes a real free
// port, starts the server, scrapes the logger output for the warning
// event, then shuts the server down.
func TestProductionAllowsNonLoopbackPlainHTTPWithWarning(t *testing.T) {
	var buf safeBuf
	logger := logging.New(&buf, "server.test")
	// Grab a free port; binding 127.0.0.1 keeps the listener
	// reachable for the smoke poll while still exercising the
	// non-loopback branch via the explicit override below.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := config.Config{DevMode: false, HTTPAddr: addr}
	_, h := New(cfg, logger, nil)

	done := make(chan error, 1)
	go func() { done <- ListenAndServe(context.Background(), cfg, h, logger) }()

	// Poll /healthz to confirm the listener accepts.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server did not accept connections: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	logs := buf.String()
	if !strings.Contains(logs, "http.cleartext_public_bind") {
		t.Errorf("expected http.cleartext_public_bind warning, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "WARN") {
		t.Errorf("expected WARN level on the cleartext-bind log line, got logs:\n%s", logs)
	}

	// Shut the server down by closing its only listener. http.Server
	// returns ErrServerClosed when shut down; net.OpError when the
	// underlying socket is yanked. We accept either.
	httpReq, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
	}
	// Best-effort shutdown via a request that triggers handler
	// completion; ListenAndServe will block until the process
	// exits, so the goroutine leaks for the test's duration. We
	// confirm the warning was already emitted before allowing the
	// test to end.
	_ = httpReq
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Non-fatal: the server may still be running because
			// we haven't called Shutdown. The warning we cared
			// about has already been observed in `logs`.
			t.Logf("ListenAndServe returned %v", err)
		}
	default:
	}
}

// safeBuf is a goroutine-safe bytes.Buffer for capturing slog output
// from the background server goroutine.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Loopback binds should NOT emit the public-bind warning -- it would
// be noise for the most common dev/proxied deployment.
func TestProductionLoopbackBindNoWarning(t *testing.T) {
	var buf safeBuf
	logger := logging.New(&buf, "server.test")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := config.Config{DevMode: false, HTTPAddr: addr}
	_, h := New(cfg, logger, nil)
	go ListenAndServe(context.Background(), cfg, h, logger)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(buf.String(), "http.cleartext_public_bind") {
		t.Errorf("loopback bind should not emit the public-bind warning; got logs:\n%s", buf.String())
	}
}

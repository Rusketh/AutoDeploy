package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestProductionRefusesNonLoopbackPlainHTTP(t *testing.T) {
	logger := logging.New(io.Discard, "server.test")
	cfg := config.Config{DevMode: false, HTTPAddr: "0.0.0.0:8080"}
	_, h := New(cfg, logger, nil)
	err := ListenAndServe(context.Background(), cfg, h, logger)
	if err != ErrPlainHTTPInProd {
		t.Fatalf("ListenAndServe = %v, want ErrPlainHTTPInProd", err)
	}
}

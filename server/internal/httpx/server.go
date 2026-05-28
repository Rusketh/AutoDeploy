// Package httpx wires the HTTP surface together. Routing is plain net/http
// ServeMux with the request-logging middleware below. The api and portal
// packages register their routes on the mux returned by New.
package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/logging"
)

// New builds the root http.ServeMux and returns it together with the wrapped
// handler (mux + request logger). Callers attach api/portal routes to the
// returned mux before serving.
func New(cfg config.Config, logger *slog.Logger) (*http.ServeMux, http.Handler) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/portal/", http.StatusFound)
	})

	return mux, withLogging(mux, logger)
}

// ListenAndServe starts the HTTP server. In non-dev mode it refuses to bind
// cleartext to a non-loopback address.
func ListenAndServe(ctx context.Context, cfg config.Config, h http.Handler, logger *slog.Logger) error {
	if !cfg.DevMode && !isLoopback(cfg.HTTPAddr) {
		return ErrPlainHTTPInProd
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "http.listen",
		slog.String("addr", cfg.HTTPAddr),
		slog.Bool("dev_mode", cfg.DevMode))
	return srv.ListenAndServe()
}

func withLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		ctx := logging.With(r.Context(), logger)
		next.ServeHTTP(rw, r.WithContext(ctx))
		logger.LogAttrs(r.Context(), slog.LevelInfo, "http.request",
			slog.String("actor", clientID(r)),
			slog.String("target", r.URL.Path),
			slog.String("method", r.Method),
			slog.Int("status", rw.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func clientID(r *http.Request) string {
	if h := r.Header.Get("X-AutoDeploy-Machine-UUID"); h != "" {
		return h
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	h = strings.TrimSpace(h)
	if h == "127.0.0.1" || h == "::1" || h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ErrPlainHTTPInProd is returned when DevMode=false and the configured bind
// address is not loopback. Production deployments must front the server with
// HTTPS or bind only to loopback behind a TLS terminator.
var ErrPlainHTTPInProd = httpErr("cleartext HTTP refused in production mode; configure HTTPS or bind to loopback")

type httpErr string

func (e httpErr) Error() string { return string(e) }

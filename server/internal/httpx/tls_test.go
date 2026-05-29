package httpx

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/logging"
)

func TestGenerateSelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	if err := generateSelfSigned(cert, key, "127.0.0.1:8443"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{cert, key} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", f)
		}
		// File must be owner-readable but not world-readable.
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has loose perms %o", f, info.Mode().Perm())
		}
	}
}

// In production mode with HTTPS_ADDR set but no cert/key configured,
// the server must auto-generate a self-signed cert and start the
// listener -- the previous "refuse to start" behaviour broke fresh
// installs whose env file shipped HTTPS_ADDR=0.0.0.0:443 by default.
// A clearly-marked WARN spells out the consequences.
func TestProductionHTTPSWithNoCertAutoGenerates(t *testing.T) {
	var buf safeBuf
	logger := logging.New(&buf, "server.test")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	dataDir := t.TempDir()
	cfg := config.Config{
		DevMode:   false,
		HTTPSAddr: addr,
		DataDir:   dataDir,
	}
	_, h := New(cfg, logger, nil)
	done := make(chan error, 1)
	go func() { done <- ListenAndServeTLS(context.Background(), cfg, h, logger) }()

	// Wait for the listener to accept and the cert files to land.
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	deadline := time.Now().Add(3 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = client.Get("https://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("https listener did not accept: %v\nlogs:\n%s", err, buf.String())
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Cert files must exist under <data-dir>/tls/.
	for _, n := range []string{"dev-cert.pem", "dev-key.pem"} {
		if _, err := os.Stat(filepath.Join(dataDir, "tls", n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}

	// Production-mode warning -- name the event + WARN level so an
	// operator scanning the install log sees the consequence
	// without having to look it up.
	logs := buf.String()
	if !strings.Contains(logs, "tls.self_signed_generated") {
		t.Errorf("missing tls.self_signed_generated event; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "WARN") {
		t.Errorf("expected WARN level in production; logs:\n%s", logs)
	}

	select {
	case err := <-done:
		t.Logf("ListenAndServeTLS returned %v", err)
	default:
	}
}

// Dev mode still generates the cert silently (well, at INFO) -- no
// scary WARN line for an operator deliberately running in dev mode.
func TestDevModeHTTPSWithNoCertAutoGeneratesQuietly(t *testing.T) {
	var buf safeBuf
	logger := logging.New(&buf, "server.test")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := config.Config{DevMode: true, HTTPSAddr: addr, DataDir: t.TempDir()}
	_, h := New(cfg, logger, nil)
	go ListenAndServeTLS(context.Background(), cfg, h, logger)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	logs := buf.String()
	if !strings.Contains(logs, "tls.self_signed_generated") {
		t.Errorf("expected self-signed-generated event in dev mode too; logs:\n%s", logs)
	}
	if strings.Contains(logs, `level":"WARN"`) {
		t.Errorf("dev mode should not log WARN on self-signed cert; logs:\n%s", logs)
	}
}

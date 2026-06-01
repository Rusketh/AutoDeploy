package httpx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rusketh/autodeploy/server/internal/config"
)

// ListenAndServeTLS starts an HTTPS server. If certFile or keyFile is
// empty -- regardless of DevMode -- a self-signed cert is generated
// under DataDir/tls and used. Production mode logs a loud warning when
// it falls back to the self-signed cert so the choice is auditable,
// but never refuses to start: HTTPS is opt-in, and an operator who
// asked for an HTTPS listener should get one, even if their cert
// material isn't ready yet.
func ListenAndServeTLS(ctx context.Context, cfg config.Config, h http.Handler, logger *slog.Logger) error {
	certFile := cfg.TLSCertFile
	keyFile := cfg.TLSKeyFile

	if certFile == "" || keyFile == "" {
		dir := filepath.Join(cfg.DataDir, "tls")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		certFile = filepath.Join(dir, "dev-cert.pem")
		keyFile = filepath.Join(dir, "dev-key.pem")
		if _, err := os.Stat(certFile); errors.Is(err, os.ErrNotExist) {
			if err := generateSelfSigned(certFile, keyFile, cfg.HTTPSAddr); err != nil {
				return fmt.Errorf("generate self-signed cert: %w", err)
			}
			level := slog.LevelInfo
			note := "self-signed; clients must trust this certificate"
			if !cfg.DevMode {
				level = slog.LevelWarn
				note = "self-signed cert auto-generated for production HTTPS bind; clients (browsers, agents, boot client) will fail TLS verification unless they trust this cert. Set AUTODEPLOY_TLS_CERT/KEY to a CA-signed pair, or front the server with a reverse proxy that terminates TLS"
			}
			logger.LogAttrs(ctx, level, "tls.self_signed_generated",
				slog.String("cert", certFile),
				slog.String("key", keyFile),
				slog.String("note", note),
			)
		}
	}
	srv := &http.Server{
		Addr:              cfg.HTTPSAddr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	// Graceful shutdown when ctx is cancelled.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	logger.LogAttrs(ctx, slog.LevelInfo, "https.listen",
		slog.String("addr", cfg.HTTPSAddr),
		slog.String("cert", certFile),
	)
	return srv.ListenAndServeTLS(certFile, keyFile)
}

// generateSelfSigned writes a P-256 self-signed certificate to certFile and
// the matching key to keyFile. Intended ONLY for development; the cert
// covers "localhost" and 127.0.0.1.
func generateSelfSigned(certFile, keyFile, addr string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = "localhost"
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "AutoDeploy dev"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", host},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	certPEM, err := os.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certPEM.Close()
		return err
	}
	if err := certPEM.Close(); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	keyPEM, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		_ = keyPEM.Close()
		return err
	}
	return keyPEM.Close()
}

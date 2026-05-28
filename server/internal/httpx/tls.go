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

// ListenAndServeTLS starts an HTTPS server. If certFile or keyFile is empty
// and DevMode is true, it generates a self-signed certificate at the
// configured paths (or under DataDir/tls if unset) and uses that. In
// production both files MUST be supplied.
func ListenAndServeTLS(ctx context.Context, cfg config.Config, h http.Handler, logger *slog.Logger) error {
	certFile := cfg.TLSCertFile
	keyFile := cfg.TLSKeyFile

	if certFile == "" || keyFile == "" {
		if !cfg.DevMode {
			return errors.New("AUTODEPLOY_TLS_CERT and AUTODEPLOY_TLS_KEY must be set in production mode")
		}
		dir := filepath.Join(cfg.DataDir, "tls")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		certFile = filepath.Join(dir, "dev-cert.pem")
		keyFile = filepath.Join(dir, "dev-key.pem")
		if _, err := os.Stat(certFile); errors.Is(err, os.ErrNotExist) {
			if err := generateSelfSigned(certFile, keyFile, cfg.HTTPSAddr); err != nil {
				return fmt.Errorf("generate dev cert: %w", err)
			}
			logger.LogAttrs(ctx, slog.LevelWarn, "tls.dev_cert_generated",
				slog.String("cert", certFile),
				slog.String("key", keyFile),
				slog.String("note", "self-signed; clients must trust this certificate"),
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

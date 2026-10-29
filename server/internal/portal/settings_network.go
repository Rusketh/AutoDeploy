package portal

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/runtime"
)

func networkForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var cfg runtime.NetworkConfig
		if r.Runtime != nil {
			cfg = r.Runtime.NetworkConfig()
		}
		networkRender(w, req, r, cfg, nil)
	}
}

// networkRender renders the network page with cfg in the fields (stored,
// or as submitted after a rejected save — fe set, 422).
func networkRender(w http.ResponseWriter, req *http.Request, r Repos, cfg runtime.NetworkConfig, fe *FormError) {
	var env runtime.NetworkConfig
	if r.Runtime != nil {
		e := r.Runtime.EnvDefaults()
		env = runtime.NetworkConfig{
			HTTPAddr:    e.HTTPAddr,
			HTTPSAddr:   e.HTTPSAddr,
			TLSCertPath: e.TLSCertFile,
			TLSKeyPath:  e.TLSKeyFile,
		}
	}
	var certInfo string
	if path := cfg.TLSCertPath; path != "" {
		certInfo = describeCert(path)
	} else if path := env.TLSCertPath; path != "" {
		certInfo = describeCert(path)
	}
	renderForm(w, req, r, "settings_network.html", "Network", fe, map[string]any{
		"Cfg":      cfg,
		"Env":      env,
		"CertInfo": certInfo,
	})
}

// validateNetworkConfig checks the network form before anything is saved.
func validateNetworkConfig(cfg runtime.NetworkConfig) *FormError {
	fe := &FormError{Fields: map[string]string{}}
	for _, f := range []struct{ field, val string }{{"http_addr", cfg.HTTPAddr}, {"https_addr", cfg.HTTPSAddr}} {
		if f.val == "" {
			continue
		}
		if _, port, err := net.SplitHostPort(f.val); err != nil || port == "" {
			fe.Fields[f.field] = "Use host:port, e.g. 0.0.0.0:8080 (all interfaces) or 127.0.0.1:8080 (this computer only)."
		}
	}
	if cfg.ExternalURL != "" && !strings.HasPrefix(cfg.ExternalURL, "http://") && !strings.HasPrefix(cfg.ExternalURL, "https://") {
		fe.Fields["external_url"] = "Start the address with https:// (or http://), e.g. https://deploy.example.com."
	}
	for _, part := range strings.Split(cfg.TrustedProxies, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(part); err != nil && net.ParseIP(part) == nil {
			fe.Fields["trusted_proxies"] = fmt.Sprintf("%q isn't an IP address or range. Use ranges like 10.0.0.0/8, separated by commas.", part)
			break
		}
	}
	for _, f := range []struct{ field, val string }{{"tls_cert_path", cfg.TLSCertPath}, {"tls_key_path", cfg.TLSKeyPath}} {
		if f.val != "" && !filepath.IsAbs(f.val) {
			fe.Fields[f.field] = "Use a full path starting from the root, e.g. /etc/ssl/certs/autodeploy.pem."
		}
	}
	if len(fe.Fields) == 0 {
		return nil
	}
	fe.Message = "Couldn't save — fix the highlighted fields below. Nothing was changed."
	return fe
}

func networkSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		extAccess := strings.TrimSpace(req.FormValue("external_access"))
		if extAccess != "agent" {
			extAccess = "full"
		}
		cfg := runtime.NetworkConfig{
			HTTPAddr:       strings.TrimSpace(req.FormValue("http_addr")),
			HTTPSAddr:      strings.TrimSpace(req.FormValue("https_addr")),
			ExternalURL:    strings.TrimSpace(req.FormValue("external_url")),
			TrustedProxies: strings.TrimSpace(req.FormValue("trusted_proxies")),
			ExternalAccess: extAccess,
			TLSCertPath:    strings.TrimSpace(req.FormValue("tls_cert_path")),
			TLSKeyPath:     strings.TrimSpace(req.FormValue("tls_key_path")),
		}
		if fe := validateNetworkConfig(cfg); fe != nil {
			networkRender(w, req, r, cfg, fe)
			return
		}
		cfg.ExternalURL = strings.TrimRight(cfg.ExternalURL, "/")
		if err := r.Runtime.SetNetworkConfig(req.Context(), cfg); err != nil {
			networkRender(w, req, r, cfg, newFormError(req, "", err))
			return
		}
		flash(w, "ok", "Network settings saved. Listen address and TLS changes take effect on next server restart.")
		http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
	}
}

func networkUploadCert(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			flash(w, "err", "Failed to parse upload: "+err.Error())
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		tlsDir := filepath.Join(r.DataDir, "tls")
		if err := os.MkdirAll(tlsDir, 0o700); err != nil {
			flash(w, "err", "Cannot create TLS directory: "+err.Error())
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		certPath, err := saveUploadedFile(req, "cert_file", filepath.Join(tlsDir, "portal-cert.pem"))
		if err != nil {
			flash(w, "err", "Certificate: "+err.Error())
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		keyPath, err := saveUploadedFile(req, "key_file", filepath.Join(tlsDir, "portal-key.pem"))
		if err != nil {
			flash(w, "err", "Private key: "+err.Error())
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		if certPath != "" && keyPath == "" {
			flash(w, "err", "Both certificate and private key must be uploaded together.")
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}
		if keyPath != "" && certPath == "" {
			flash(w, "err", "Both certificate and private key must be uploaded together.")
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		if certPath == "" && keyPath == "" {
			flash(w, "err", "No files selected.")
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}

		cfg := r.Runtime.NetworkConfig()
		cfg.TLSCertPath = certPath
		cfg.TLSKeyPath = keyPath

		if err := r.Runtime.SetNetworkConfig(req.Context(), cfg); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "TLS certificate and key uploaded. Restart the server to use them.")
		}
		http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
	}
}

func saveUploadedFile(req *http.Request, field, dest string) (string, error) {
	f, hdr, err := req.FormFile(field)
	if err != nil {
		if err == http.ErrMissingFile {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	_ = hdr

	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}

	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}
	return dest, nil
}

func describeCert(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Cannot read: " + err.Error()
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "Invalid PEM"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "Parse error: " + err.Error()
	}
	names := cert.DNSNames
	if cert.Subject.CommonName != "" {
		found := false
		for _, n := range names {
			if n == cert.Subject.CommonName {
				found = true
				break
			}
		}
		if !found {
			names = append([]string{cert.Subject.CommonName}, names...)
		}
	}
	return fmt.Sprintf("CN=%s, Names=%s, Expires=%s",
		cert.Subject.CommonName,
		strings.Join(names, ", "),
		cert.NotAfter.Format("2006-01-02"))
}

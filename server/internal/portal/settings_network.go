package portal

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/runtime"
)

func networkForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var cfg runtime.NetworkConfig
		var env runtime.NetworkConfig
		if r.Runtime != nil {
			cfg = r.Runtime.NetworkConfig()
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

		render(w, req, r, "settings_network.html", "Network", map[string]any{
			"Cfg":      cfg,
			"Env":      env,
			"CertInfo": certInfo,
		})
	}
}

func networkSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

		if cfg.ExternalURL != "" && !strings.HasPrefix(cfg.ExternalURL, "http://") && !strings.HasPrefix(cfg.ExternalURL, "https://") {
			flash(w, "err", "External URL must start with http:// or https://")
			http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
			return
		}
		if cfg.ExternalURL != "" {
			cfg.ExternalURL = strings.TrimRight(cfg.ExternalURL, "/")
		}

		if err := r.Runtime.SetNetworkConfig(req.Context(), cfg); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Network settings saved. Listen address and TLS changes take effect on next server restart.")
		}
		http.Redirect(w, req, "/portal/settings/network", http.StatusFound)
	}
}

func networkUploadCert(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
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

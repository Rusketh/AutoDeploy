// Package config loads server configuration from the environment. Keep this
// small and predictable; the operational documentation is the source of truth
// for what each setting means.
package config

import (
	"errors"
	"os"
	"strconv"
)

// Config is the resolved server configuration.
type Config struct {
	// HTTPAddr is the address the cleartext HTTP server binds to (host:port).
	// Empty disables HTTP. In production mode, only loopback is permitted.
	HTTPAddr string
	// HTTPSAddr is the address the HTTPS server binds to. Empty disables
	// HTTPS.
	HTTPSAddr string
	// TLSCertFile and TLSKeyFile point to a PEM cert/key pair. When empty
	// and DevMode is true, a self-signed cert is generated under DataDir/tls.
	TLSCertFile string
	TLSKeyFile  string
	// DataDir is the on-disk root for payload blobs.
	DataDir string
	// DevMode permits cleartext HTTP on non-loopback addresses and enables
	// self-signed-cert generation. Production deployments must set false.
	DevMode bool
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:    getenv("AUTODEPLOY_HTTP_ADDR", "127.0.0.1:8080"),
		HTTPSAddr:   getenv("AUTODEPLOY_HTTPS_ADDR", ""),
		TLSCertFile: getenv("AUTODEPLOY_TLS_CERT", ""),
		TLSKeyFile:  getenv("AUTODEPLOY_TLS_KEY", ""),
		DataDir:     getenv("AUTODEPLOY_DATA_DIR", "./data"),
	}
	dev, err := strconv.ParseBool(getenv("AUTODEPLOY_DEV", "true"))
	if err != nil {
		return Config{}, errors.New("AUTODEPLOY_DEV must be a boolean")
	}
	c.DevMode = dev
	return c, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

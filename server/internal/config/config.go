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
	// HTTPAddr is the address the HTTP server binds to (host:port).
	HTTPAddr string
	// DataDir is the on-disk root for payload blobs (extracted ISO contents,
	// driver packages, software installers).
	DataDir string
	// DevMode permits cleartext HTTP on non-loopback addresses. Production
	// deployments must set this to false.
	DevMode bool
}

// Load reads configuration from the environment. Missing values use sensible
// defaults suitable for a development build.
func Load() (Config, error) {
	c := Config{
		HTTPAddr: getenv("AUTODEPLOY_HTTP_ADDR", "127.0.0.1:8080"),
		DataDir:  getenv("AUTODEPLOY_DATA_DIR", "./data"),
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

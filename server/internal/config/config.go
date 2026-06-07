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

	// Active Directory integration (Phase 10). Empty ADLDAPURL disables AD.
	ADLDAPURL       string
	ADBindDN        string
	ADBindPassword  string
	ADSearchBase    string
	ADSkipTLSVerify bool

	// Secrets-at-rest encryption key (Phase 12). Hex-encoded 32 bytes.
	// Empty causes the server to load (or generate) a key file under
	// AUTODEPLOY_DATA_DIR/secrets-key.bin with 0600 perms.
	SecretsKeyHex string

	// Phase 16. Log retention (days). 0 disables pruning.
	LogRetentionDays int

	// Mass-scale: bound concurrent /payload/* streams (default 64).
	// Zero = unlimited (don't pick this on a production node — a
	// 500-machine PXE burst will exhaust the file-descriptor budget).
	PayloadMaxInFlight int

	// TFTP listener address (e.g. ":69"). Empty disables the built-in
	// TFTP server. Serves $AUTODEPLOY_DATA_DIR/ipxe read-only so a
	// classic PXE setup can fetch the iPXE bootstrap binaries
	// (undionly.kpxe, ipxe.efi, …) without a separate TFTP daemon.
	TFTPAddr string
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
	dev, err := strconv.ParseBool(getenv("AUTODEPLOY_DEV", "false"))
	if err != nil {
		return Config{}, errors.New("AUTODEPLOY_DEV must be a boolean")
	}
	c.DevMode = dev
	c.ADLDAPURL = getenv("AUTODEPLOY_AD_URL", "")
	c.ADBindDN = getenv("AUTODEPLOY_AD_BIND_DN", "")
	c.ADBindPassword = getenv("AUTODEPLOY_AD_BIND_PASSWORD", "")
	c.ADSearchBase = getenv("AUTODEPLOY_AD_SEARCH_BASE", "")
	skip, err := strconv.ParseBool(getenv("AUTODEPLOY_AD_SKIP_TLS_VERIFY", "false"))
	if err != nil {
		return Config{}, errors.New("AUTODEPLOY_AD_SKIP_TLS_VERIFY must be a boolean")
	}
	c.ADSkipTLSVerify = skip
	c.SecretsKeyHex = getenv("AUTODEPLOY_SECRETS_KEY", "")
	days, err := strconv.Atoi(getenv("AUTODEPLOY_LOG_RETENTION_DAYS", "0"))
	if err != nil {
		return Config{}, errors.New("AUTODEPLOY_LOG_RETENTION_DAYS must be an integer")
	}
	c.LogRetentionDays = days
	maxIF, err := strconv.Atoi(getenv("AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT", "128"))
	if err != nil {
		return Config{}, errors.New("AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT must be an integer")
	}
	c.PayloadMaxInFlight = maxIF
	c.TFTPAddr = getenv("AUTODEPLOY_TFTP_ADDR", "")
	return c, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

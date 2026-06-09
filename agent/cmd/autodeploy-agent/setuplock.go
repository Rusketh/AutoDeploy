package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
)

// This file implements the agent half of the setup-lock screen: it maintains
// the on-disk state the Windows credential provider reads (an "active" marker
// that arms the lock, a status.json that drives the progress bar + activity
// line, and a branding.json), and services technician-unlock PIN checks. All
// state lives under a fixed directory the provider and SetupComplete.cmd agree
// on, so the two processes -- which run in different sessions -- coordinate
// through the filesystem rather than a cross-session pipe.

// lockStateDir is where the credential provider looks for its state. It MUST
// match the fixed path SetupComplete.cmd seeds and the provider reads.
// Overridable in tests.
var lockStateDir = defaultLockStateDir()

func defaultLockStateDir() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\lock`
	}
	// Dev/non-Windows hosts: a predictable local path (the lock only runs for
	// real on Windows).
	return "/var/lib/autodeploy/lock"
}

func lockPath(name string) string { return filepath.Join(lockStateDir, name) }

// lockStatus is the JSON the credential provider reads to render the branded
// setup screen. Written atomically on every progress update.
type lockStatus struct {
	Phase     string `json:"phase"`    // e.g. "installing", "agent-online"
	Activity  string `json:"activity"` // human text, e.g. "Installing Microsoft Office"
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	Percent   int    `json:"percent"` // 0..100; -1 means indeterminate
	UpdatedAt string `json:"updated_at"`
}

// lockBranding is the subset of server branding the provider renders. Field
// names mirror server branding.Brand so the JSON round-trips unchanged.
type lockBranding struct {
	ProductName      string `json:"product_name"`
	OrganisationName string `json:"organisation_name"`
	SupportURL       string `json:"support_url"`
	SupportPhone     string `json:"support_phone"`
	LogoDataURL      string `json:"logo_data_url"`
	PrimaryColor     string `json:"primary_color"`
}

// writeFileAtomic writes data via a temp file + rename so a reader (the
// credential provider) never observes a half-written file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func lockMarkerPath() string { return lockPath("active") }

// lockMarkerPresent reports whether the lock is armed (the provider should be
// covering the logon screen).
func lockMarkerPresent() bool {
	_, err := os.Stat(lockMarkerPath())
	return err == nil
}

// armLockMarker creates the "active" marker so the credential provider locks
// the logon screen. Idempotent. SetupComplete.cmd also arms it at deploy time;
// the agent re-arms defensively while the initial deployment is open.
func armLockMarker() error { return writeFileAtomic(lockMarkerPath(), []byte("1")) }

// clearLockMarker removes the marker so the next logon shows the normal
// Windows screen. Called once the agent closes the initial deployment.
func clearLockMarker() { _ = os.Remove(lockMarkerPath()) }

// writeLockStatus atomically updates status.json so the provider's progress
// bar and activity line stay live. A total of 0 renders an indeterminate bar.
func writeLockStatus(phase, activity string, done, total int) {
	percent := -1
	if total > 0 {
		percent = done * 100 / total
		if percent > 100 {
			percent = 100
		}
		if percent < 0 {
			percent = 0
		}
	}
	b, err := json.Marshal(lockStatus{
		Phase:     phase,
		Activity:  activity,
		Done:      done,
		Total:     total,
		Percent:   percent,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = writeFileAtomic(lockPath("status.json"), b)
}

// refreshLockBranding fetches /api/v1/branding and writes branding.json for the
// provider. Best-effort: the provider falls back to built-in defaults when the
// file is absent.
func refreshLockBranding(ctx context.Context, log *slog.Logger, c *httpc.Client) {
	var b lockBranding
	if err := c.GetJSON(ctx, "/api/v1/branding", &b); err != nil {
		log.Info("lock.brand.skip", slog.String("reason", err.Error()))
		return
	}
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	_ = writeFileAtomic(lockPath("branding.json"), data)
}

// pinValidator decides whether an entered technician PIN unlocks the screen.
type pinValidator func(ctx context.Context, pin string) bool

// serverPINValidator validates a PIN against the server's existing
// rate-limited gate (the same endpoint the boot client uses). When no PIN is
// configured server-side, the endpoint grants any submission, so the provider
// can offer an unconditional unlock without special-casing it here.
func serverPINValidator(c *httpc.Client, f agentFlags) pinValidator {
	return func(ctx context.Context, pin string) bool {
		var r struct {
			Granted bool `json:"granted"`
		}
		if err := c.PostJSON(ctx, "/api/v1/clients/validate-pin", map[string]any{
			"system_uuid": f.uuid,
			"pin":         pin,
		}, &r); err != nil {
			return false
		}
		return r.Granted
	}
}

// watchLockPINRequests services technician-unlock requests from the credential
// provider over a tiny file protocol in the lock dir (the provider and the
// agent run in different sessions, so a shared SYSTEM-only dir is the simplest
// reliable channel):
//
//	pin-request  -- written by the provider, contains the entered PIN
//	pin-response -- written by the agent, "allow" or "deny"
//
// Validation is delegated here (Go) so the provider holds no crypto; the
// server's per-machine rate limiting backstops brute force. Runs until ctx is
// cancelled; cheap (a single Stat per tick).
func watchLockPINRequests(ctx context.Context, log *slog.Logger, validate pinValidator, onUnlock func()) {
	const interval = 300 * time.Millisecond
	reqPath := lockPath("pin-request")
	respPath := lockPath("pin-response")
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		data, err := os.ReadFile(reqPath)
		if err != nil {
			continue // nothing pending
		}
		_ = os.Remove(reqPath) // consume once
		pin := strings.TrimRight(string(data), "\r\n")
		allowed := validate(ctx, pin)
		result := "deny"
		if allowed {
			result = "allow"
		}
		if err := writeFileAtomic(respPath, []byte(result)); err != nil {
			log.Warn("lock.pin.respond", slog.String("error", err.Error()))
			continue
		}
		log.Info("lock.pin.checked", slog.String("result", result))
		if allowed && onUnlock != nil {
			onUnlock() // clear the marker + reboot to a clean logon
		}
	}
}

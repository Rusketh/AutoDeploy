// Package selfupdate handles the agent's in-place upgrade. The
// design is:
//
//  1. The agent POSTs its current version and platform to
//     /api/v1/agent/update-info on each check-in.
//  2. The server scans its downloads category for a newer agent
//     binary matching the platform and returns its URL + expected
//     SHA-256 if available.
//  3. The agent downloads the new binary to a sibling ".new" file,
//     verifies the SHA-256 against the response, then spawns a
//     small platform-specific updater script that:
//     - waits for the agent process to exit (we exit moments after
//     spawning the updater),
//     - renames the .new file over the live executable,
//     - relaunches the agent with the original command-line.
//
// The SHA-256 verification is the only authenticator -- never
// execute a downloaded binary without verifying it against the
// hash the trusted server told us about.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// UpdateInfo mirrors the server's /api/v1/agent/update-info response.
type UpdateInfo struct {
	Current         string `json:"current_version"`
	UpdateAvailable bool   `json:"update_available"`
	URL             string `json:"url,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	Size            int64  `json:"size_bytes,omitempty"`
}

// Download fetches url to dst and confirms the SHA-256 matches
// wantHex. Returns an error if the download truncates, the hash is
// wrong, or any I/O fails.
func Download(ctx context.Context, url, dst, wantHex string, insecureTLS bool) error {
	if url == "" || wantHex == "" {
		return errors.New("download: url and sha256 required")
	}
	client := &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s -> %d", url, resp.StatusCode)
	}
	// Write to a tmp sibling first so an interrupted download
	// doesn't leave a half-baked .new file the next swap would
	// mistake for valid.
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("download: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHex {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: sha256 mismatch (got %s want %s)", got, wantHex)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SiblingPath returns the path next to the running executable with
// the given suffix appended. Used to pick where to download the
// .new file and where to write the updater script.
func SiblingPath(suffix string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return exe + suffix, nil
}

// CurrentExe returns the absolute path of the running executable,
// with symlinks resolved.
func CurrentExe() (string, error) {
	return SiblingPath("")
}

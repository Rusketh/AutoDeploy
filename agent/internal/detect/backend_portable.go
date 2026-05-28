//go:build !windows

package detect

import "os"

// PortableBackend is the non-Windows backend used for dev hosts and tests.
// File operations work normally; registry and MSI rules always return
// "not present" (so the agent will re-install, which is the safe default
// off-Windows).
type PortableBackend struct{}

func (PortableBackend) FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (PortableBackend) FileVersion(string) (string, error) {
	// No version info available off-Windows. Returning "" causes the
	// file-version compare in detect.evaluateFile to fail closed (i.e.
	// not present), which is the safe default.
	return "", nil
}

func (PortableBackend) RegistryValuePresent(string, string, string) (bool, string, error) {
	return false, "", nil
}

func (PortableBackend) MSIProductInstalled(string) (bool, error) {
	return false, nil
}

// DefaultBackend returns the host's preferred backend. Windows builds
// override this in backend_windows.go.
func DefaultBackend() Backend { return PortableBackend{} }

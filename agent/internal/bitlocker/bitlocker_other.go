//go:build !windows

package bitlocker

import "context"

// driverEnable on non-Windows hosts returns ErrUnsupported so the agent
// logs the situation and continues (or aborts) — it never pretends
// encryption happened.
func driverEnable(_ context.Context, _, _ string) (string, error) {
	return "", ErrUnsupported
}

func driverState(_ context.Context, _ string) (State, error) {
	return State{}, ErrUnsupported
}

func driverChangePIN(_ context.Context, _, _ string) error {
	return ErrUnsupported
}

func driverDisable(_ context.Context, _ string) error {
	return ErrUnsupported
}

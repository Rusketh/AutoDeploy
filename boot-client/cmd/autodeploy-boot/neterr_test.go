package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestNetErrCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		// The idle watchdog aborts a silent stall by cancelling the request;
		// the surfaced error must read as a stall, not a generic blip.
		{"idle-cancel", fmt.Errorf("Get %q: %w", "https://srv/blob", context.Canceled), "stalled, no data"},
		{"deadline", context.DeadlineExceeded, "stalled, no data"},
		{"io-timeout", errors.New("read tcp 10.0.0.2:5: i/o timeout"), "stalled, no data"},
		{"stall-budget", errors.New("download /mnt/x stalled for 30m0s: x"), "stalled, no data"},
		{"reset", errors.New("read tcp: connection reset by peer"), "connection reset"},
		{"pipe", errors.New("write: broken pipe"), "connection dropped"},
		{"refused", errors.New("dial tcp: connection refused"), "connection refused"},
		{"unreachable", errors.New("connect: network is unreachable"), "network unreachable"},
		{"short", errors.New("short read: have 5 of 9 bytes"), "ended early"},
		{"dns", errors.New("lookup srv: no such host"), "name lookup failed"},
		{"other", errors.New("tls: handshake failure"), "network error"},
		{"nil", nil, "no data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netErrCause(tc.err); got != tc.want {
				t.Errorf("netErrCause(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// netErrCause must never leak the server URL onto the progress screen.
func TestNetErrCauseNeverLeaksURL(t *testing.T) {
	err := fmt.Errorf("GET https://deploy.internal:8443/payload/iso/7/install.swm: connection reset by peer")
	if got := netErrCause(err); got == err.Error() {
		t.Fatal("netErrCause echoed the raw error (would reveal the server URL)")
	}
}

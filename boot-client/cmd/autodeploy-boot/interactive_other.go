//go:build !linux

package main

import (
	"context"
	"log/slog"

	"github.com/rusketh/autodeploy/boot-client/internal/httpc"
	"github.com/rusketh/autodeploy/boot-client/internal/logging"
	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
)

// startInteractive has no GUI off Linux (the boot client only ever runs in
// the Linux initramfs); always fall back to the console.
func startInteractive(ctx context.Context, log *slog.Logger, f bootFlags, c *httpc.Client, id smbios.Identity, resp menuResponse, brand brandResp, shipper *logging.Shipper) bool {
	return false
}

// bootCountdown has no GUI off Linux; use the text-console countdown.
func bootCountdown(log *slog.Logger, brand brandResp, seconds int) bool {
	return consoleBootCountdown(seconds)
}

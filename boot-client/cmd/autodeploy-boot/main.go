// Command autodeploy-boot is the Boot Client. In Phase 0 it does the bare
// minimum: read SMBIOS identity, log it, and exit. The pre-OS imaging flow
// (menu, image apply, driver inject, unattend place) lands in Phase 3.
//
// The Boot Client never makes resolution or access decisions itself; the
// server is the sole authority. This binary is a fact reporter and a step
// executor.
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/rusketh/autodeploy/boot-client/internal/logging"
	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
)

func main() {
	server := flag.String("server", "", "AutoDeploy server base URL")
	sysfs := flag.String("sysfs", "/sys/class/dmi/id", "DMI sysfs root (override for testing)")
	flag.Parse()

	log := logging.New(os.Stdout, "boot")
	log.Info("boot.start", slog.String("actor", "system"), slog.String("server", *server))

	id, err := smbios.ReadFromSysfs(*sysfs)
	if err != nil {
		log.Error("smbios.read", slog.String("error", err.Error()))
		os.Exit(1)
	}
	log.Info("smbios.read",
		slog.String("actor", id.SystemUUID),
		slog.String("target", "self"),
		slog.String("manufacturer", id.SystemManufacturer),
		slog.String("product", id.SystemProduct),
		slog.String("serial", id.SystemSerial),
	)
	// Fail-safe by default: with no server configured we do nothing destructive.
	if *server == "" {
		log.Info("boot.idle", slog.String("reason", "no server configured; exiting safely"))
		return
	}
}

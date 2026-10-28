package model

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Log categories: the coarse "what is this about" bucket shown on every log
// row and offered as a filter on the Logs page.
const (
	LogCatDeployment = "Deployment"
	LogCatSoftware   = "Software"
	LogCatUpdates    = "Updates"
	LogCatDomain     = "Domain"
	LogCatSecurity   = "Security"
	LogCatAdmin      = "Admin"
	LogCatSystem     = "System"
)

// LogCategories lists every category in display order.
var LogCategories = []string{
	LogCatDeployment, LogCatSoftware, LogCatUpdates, LogCatDomain,
	LogCatSecurity, LogCatAdmin, LogCatSystem,
}

// logEntry is one action's catalogue row.
//
// Msg is a small template: {name} is replaced by fields[name] (or one of the
// specials target, machine, user, error); [ ... ] is an optional segment that
// is dropped when any placeholder inside it has no value; "A || B" uses A
// unless one of its required placeholders is missing, then tries B. A Msg
// that ends up empty falls back to the humanised action code.
type logEntry struct {
	Msg string
	Cat string
	// Level, when set, replaces the emitter's level at read time (e.g. older
	// agents logged every stderr line as WARN; it is just output).
	Level string
	// Hint is what to try next; HintFn computes it from the fields instead.
	Hint   string
	HintFn func(f map[string]any) string
	// Outcome is "ok" / "failed" for events that report a result.
	Outcome string
}

// e builds an entry; the helpers keep the table below readable.
func e(cat, msg string) logEntry { return logEntry{Msg: msg, Cat: cat} }
func eFail(cat, msg, hint string) logEntry {
	return logEntry{Msg: msg, Cat: cat, Hint: hint, Outcome: "failed"}
}
func eOK(cat, msg string) logEntry { return logEntry{Msg: msg, Cat: cat, Outcome: "ok"} }

// Common hint text, shared by several actions.
const (
	hintNetwork  = "Check the machine's network connection and that it can reach the AutoDeploy server, then retry."
	hintRetryRun = "Retry the deployment; if it fails the same way again, check the item in the portal."
	hintHash     = "The downloaded file doesn't match what the server advertised. Re-upload or re-publish it on the server and retry."
)

// bootFailureHints maps the boot client's error_code to what to try next.
var bootFailureHints = map[string]string{
	"NO-DISK":        "Check the disk shows up in the BIOS/UEFI setup and that the storage controller is in AHCI mode (not RAID/Intel RST) — the boot environment may have no driver for it.",
	"MANIFEST":       "Check the image still exists and is complete in the portal (its ISO extracted, every package uploaded), then retry.",
	"WORKDIR":        "The boot environment couldn't create its working folder — usually not enough RAM. Retry, or give the machine more memory.",
	"PAYLOAD":        "Check the file still exists on the server and the network is stable, then retry.",
	"NO-MEDIA":       "Edit the image in the portal and attach an ISO that has been uploaded and extracted.",
	"MEDIA-INDEX":    "Re-extract the image's ISO in the portal, then retry.",
	"PARTITION":      "The disk may be in use, write-protected or failing. Retry; if it keeps failing check the disk's health or replace it.",
	"MEDIA-DOWNLOAD": hintNetwork,
	"MEDIA-STAGE":    "Check the disk is large enough for the Windows media and is healthy, then retry.",
	"REBOOT":         "Restart the machine by hand; it continues into Windows Setup.",
	"MENU-FETCH":     "Check the network cable, DHCP and firewall between this machine and the AutoDeploy server, then retry.",
}

// bootFailureHint is HintFn for the boot client's failure actions.
func bootFailureHint(f map[string]any) string {
	return bootFailureHints[fieldString(f, "error_code")]
}

// packageFailHint suggests a next step from the installer's exit code.
func packageFailHint(f map[string]any) string {
	code, ok := fieldInt(f, "exit_code")
	if !ok {
		return "Run the package's install command by hand on a test machine to see why it fails."
	}
	switch code {
	case 1603:
		return "Windows Installer hit a fatal error. Common causes: an older version that can't be removed, a missing prerequisite, or a pending reboot. Check the installer's own log on the machine."
	case 1618:
		return "Another installation was already running. Retry once it has finished (or after a reboot)."
	case 1619, 1620:
		return "The installer file couldn't be opened — re-upload the package in the portal."
	case 1625:
		return "A system policy blocks this install. Check Group Policy / AppLocker on the machine."
	case 1638:
		return "Another version is already installed. Uninstall it first, or add a detection rule so the package is skipped when present."
	case 1602:
		return "The installer was cancelled — make sure the install command runs silently (e.g. /qn)."
	}
	return "Run the package's install command by hand on a test machine to see why it fails."
}

// msiExitMeanings are the Windows Installer exit codes package installers
// most often return. Mirrors the agent's steps.ExitMeaning so rows from
// agents that don't send exit_meaning still read well.
var msiExitMeanings = map[int]string{
	0:    "success",
	1602: "install cancelled",
	1603: "fatal error during installation",
	1618: "another installation is already in progress",
	1619: "installer package could not be opened",
	1620: "installer package is invalid",
	1625: "installation blocked by system policy",
	1638: "another version of this product is already installed",
	1641: "success, restart initiated",
	3010: "success, restart required",
}

// MSIExitMeaning returns the plain-English meaning of a Windows Installer
// exit code, or "" when unknown.
func MSIExitMeaning(code int) string { return msiExitMeanings[code] }

// bootFail is the shared entry for the boot client's failure actions, which
// newer clients send with a plain-English fields.message.
func bootFail(fallback string) logEntry {
	return logEntry{Msg: "{message} || " + fallback, Cat: LogCatDeployment, HintFn: bootFailureHint, Outcome: "failed"}
}

// logCatalog is every action code the server, boot client and agent log,
// keyed by action. TestLogCatalogCoversSource keeps it complete.
var logCatalog = map[string]logEntry{
	// ---- Boot client: start-up, menu, PIN -------------------------------
	"boot.start":                        e(LogCatDeployment, "Boot client started[ (version {version})][ on a {manufacturer} {product}]"),
	"boot.idle":                         e(LogCatDeployment, "Boot client has no server configured; booting normally"),
	"boot.unknown_cmd":                  eFail(LogCatDeployment, "Boot client was started with an unknown command[ “{cmd}”]", "Check the PXE / iPXE boot entry's command line."),
	"boot.active_install.detected":      e(LogCatDeployment, "Windows Setup is already running on this machine[ (seen {bounces} times)]"),
	"boot.active_install.bounce":        e(LogCatDeployment, "Handing back to the Windows Setup already in progress[ (attempt {attempt})]"),
	"boot.active_install.bounce_failed": eFail(LogCatDeployment, "Couldn't hand back to the Windows Setup in progress[: {error}]", "Restart the machine and let it boot from its disk."),
	"boot.active_install.giveup":        e(LogCatDeployment, "Gave up waiting for the Windows Setup in progress[ after {bounces} attempts]; showing the menu"),
	"boot.active_install.override":      e(LogCatDeployment, "Operator chose to re-image although Windows Setup was in progress"),
	"boot.active_install.reboot":        eFail(LogCatDeployment, "Couldn't restart into the Windows Setup in progress[: {error}]", "Restart the machine by hand."),
	"smbios.read":                       eFail(LogCatDeployment, "Couldn't read the machine's hardware identity (SMBIOS)[: {error}]", "Update the machine's BIOS/UEFI firmware; the boot client needs a readable SMBIOS UUID."),
	"menu.fetch":                        bootFail("Couldn't fetch the deployment menu from the server[: {error}]"),
	"menu.auto_reimage":                 e(LogCatDeployment, "Remote re-image requested; starting the deployment without the menu"),
	"menu.empty":                        e(LogCatDeployment, "No deployable images are available; booting normally"),
	"menu.cancel":                       e(LogCatDeployment, "Deployment menu closed without choosing an image[ ({reason})]"),
	"pin.validate":                      eFail(LogCatSecurity, "Couldn't check the access PIN with the server[: {error}]", hintNetwork),
	"pin.cancel":                        e(LogCatSecurity, "Access PIN prompt cancelled[ ({reason})]"),
	"pin.granted":                       eOK(LogCatSecurity, "Correct access PIN entered"),
	"pin.failed":                        {Msg: "Wrong access PIN entered[ (attempt {attempt})]", Cat: LogCatSecurity, Level: "WARN"},
	"pin.locked_out":                    {Msg: "Too many wrong PINs; the console is locked out for a while", Cat: LogCatSecurity, Level: "WARN"},
	"gui.unavailable":                   e(LogCatDeployment, "Graphical screen unavailable; using the text console[ ({reason})]"),
	"gui.theme":                         e(LogCatDeployment, "Couldn't load the screen theme; using the default[: {error}]"),
	"gui.noinput":                       e(LogCatDeployment, "No keyboard/mouse found for the graphical screen[ ({reason})]"),
	"gui.cancel":                        e(LogCatDeployment, "Graphical menu closed[ ({reason})]"),
	"gui.progress.disabled":             e(LogCatDeployment, "Graphical progress screen turned off[ ({reason})]"),
	"gui.pin.lockout":                   e(LogCatSecurity, "PIN screen locked after too many wrong attempts"),
	"console.debug.enabled":             e(LogCatSystem, "Debug console enabled on the machine"),

	// ---- Boot client: deployment ---------------------------------------
	"deploy":                     eFail(LogCatDeployment, "Boot client couldn't start the deployment[: {error}]", "Check the boot entry passes the server URL and image ID."),
	"manifest.fetch":             bootFail("Couldn't fetch the deployment plan (manifest) from the server[: {error}]"),
	"manifest.received":          e(LogCatDeployment, "Received the deployment plan for {target}[ ({items} items)]"),
	"workdir.create":             bootFail("Couldn't create the working folder[: {error}]"),
	"download":                   bootFail("Couldn't download a deployment file from the server[: {error}]"),
	"download.retry":             e(LogCatDeployment, "Retrying a download[ ({role})][, attempt {attempt}][: {error}]"),
	"download.progress":          e(LogCatDeployment, "Downloading[ {role}][ ({bytes} bytes so far)]"),
	"download.media":             bootFail("Couldn't download the Windows install media[: {error}]"),
	"download.media.progress":    e(LogCatDeployment, "Downloading Windows install media[: {done} of {total} files]"),
	"download.media.retry":       e(LogCatDeployment, "Retrying a media file[ {file}][ (attempt {attempt})][: {error}]"),
	"download.media.ok":          eOK(LogCatDeployment, "Windows install media downloaded[ ({files} files)]"),
	"deploy.no_media":            bootFail("This image has no install media attached"),
	"deploy.media_index.fail":    bootFail("Couldn't read the install media's file list[: {error}]"),
	"deploy.partition.fail":      bootFail("Couldn't partition the disk[ {target}][: {error}]"),
	"deploy.stage.fail":          bootFail("Couldn't copy the Windows install media to the disk[: {error}]"),
	"deploy.stage.start":         e(LogCatDeployment, "Preparing disk {target} for Windows Setup || Preparing the disk for Windows Setup"),
	"deploy.stage.ok":            eOK(LogCatDeployment, "Disk prepared; Windows Setup media staged[ on {target}]"),
	"deploy.bootentry.fail":      {Msg: "Couldn't add the Windows Setup boot entry[: {error}]", Cat: LogCatDeployment, Hint: "The machine may need to be told to boot from its disk in the firmware boot menu."},
	"deploy.reboot.skip":         e(LogCatDeployment, "Not restarting (dry run)"),
	"deploy.disk.detect":         e(LogCatDeployment, "Looking for the target disk[ (candidates: {candidates})]"),
	"deploy.disk.auto":           e(LogCatDeployment, "Using disk {disk} (the only suitable disk)"),
	"deploy.disk.multiple":       e(LogCatDeployment, "Several disks found[ ({candidates})][; using {using}]"),
	"deploy.disk.missing":        eFail(LogCatDeployment, "The requested disk[ {requested}] wasn't found[ (found: {candidates})]", "Check the image's target-disk setting matches this machine's hardware."),
	"deploy.disk.none":           bootFail("No disk found to install Windows on"),
	"deploy.disk.resolved":       e(LogCatDeployment, "Target disk[ {requested}] resolved to {using}"),
	"deploy.progress":            e(LogCatDeployment, "{stage}[: {detail}][ ({percent}%)] || Deployment progress update"),
	"deploy.progress.file":       e(LogCatDeployment, "Downloading {file}"),
	"deploy.status.report":       e(LogCatDeployment, "Couldn't report the deployment status[ “{status}”] to the server[: {error}]"),
	"deploy.fail.choice":         e(LogCatDeployment, "Technician chose “{choice}” on the failure screen"),
	"deploy.retry.exec":          eFail(LogCatDeployment, "Couldn't restart the boot client to retry[: {error}]", "Restart the machine to try again."),
	"deploy.close":               e(LogCatDeployment, "Couldn't close the deployment record[: {error}]"),
	"deploy.closed":              e(LogCatDeployment, "Deployment record closed[ ({outcome})]"),
	"reboot":                     bootFail("Couldn't restart the machine[: {error}]"),
	"media.cleanup":              e(LogCatDeployment, "Install media clean-up[: {status}]"),
	"reimage.nextboot":           e(LogCatDeployment, "Re-image armed for the next network boot[ ({status})]"),
	"imaging.exec":               e(LogCatDeployment, "Running {target}[ {args}]"),
	"imaging.exec.output":        e(LogCatDeployment, "Output from {target}"),
	"imaging.exec.stdout":        {Msg: "{line}", Cat: LogCatDeployment, Level: "INFO"},
	"imaging.exec.stderr":        {Msg: "{line}", Cat: LogCatDeployment, Level: "INFO"},
	"callback.script":            e(LogCatDeployment, "Couldn't write the Windows Setup progress script[: {error}]"),
	"install.host_os":            e(LogCatDeployment, "Installed OS is[ {os_caption}][ ({os_version})]"),
	"net.reacquire":              e(LogCatSystem, "Couldn't renew the network address[ on {if}][: {error}]"),
	"net.reacquire.ok":           e(LogCatSystem, "Network address renewed[ on {if}]"),
	"net.usbnic.replug":          e(LogCatSystem, "Re-plugged the USB network adapter[ {if}]"),
	"net.usbnic.harden":          e(LogCatSystem, "Stopped the USB network adapter[ {if}] from power-saving"),
	"net.usbnic.diag":            e(LogCatSystem, "USB network adapter[ {if}][ ({driver})] diagnostics"),
	"net.usbnic.stat":            e(LogCatSystem, "USB network adapter counter[ {if}][: {counter}]"),
	"net.usbnic.dmesg":           e(LogCatSystem, "{line}"),
	"agent.no_id":                e(LogCatDeployment, "The deployment plan had no agent ID; the agent will start without an identity"),
	"agent.setupcomplete":        e(LogCatDeployment, "Couldn't write the agent's start-up script[: {error}]"),
	"agent.update_info":          e(LogCatDeployment, "Couldn't ask the server for the agent download[: {error}]"),
	"agent.unavailable":          e(LogCatDeployment, "The server has no agent to install; the machine will deploy without one"),
	"agent.retry":                e(LogCatDeployment, "Retrying the agent download[ (attempt {attempt})][: {error}]"),
	"agent.download":             e(LogCatDeployment, "Couldn't download the agent[: {error}]"),
	"agent.invalid":              e(LogCatDeployment, "The downloaded agent isn't a valid program[ ({reason})]"),
	"agent.hash.read":            e(LogCatDeployment, "Couldn't check the agent download's checksum[: {error}]"),
	"agent.hash.mismatch":        {Msg: "The agent download is corrupt (checksum mismatch)", Cat: LogCatDeployment, Hint: hintHash},
	"agent.hash.ok":              e(LogCatDeployment, "Agent download verified"),
	"agent.hash.skipped":         e(LogCatDeployment, "Agent download not verified[ ({reason})]"),
	"agent.fetched":              eOK(LogCatDeployment, "Agent downloaded"),
	"credprovider.info":          e(LogCatDeployment, "Couldn't ask the server for the setup-lock screen[: {error}]"),
	"credprovider.unavailable":   e(LogCatDeployment, "The server has no setup-lock screen; deploying without it"),
	"credprovider.retry":         e(LogCatDeployment, "Retrying the setup-lock screen download[ (attempt {attempt})][: {error}]"),
	"credprovider.download":      e(LogCatDeployment, "Couldn't download the setup-lock screen[: {error}]"),
	"credprovider.invalid":       e(LogCatDeployment, "The setup-lock screen download is invalid[ ({reason})]"),
	"credprovider.hash.read":     e(LogCatDeployment, "Couldn't check the setup-lock screen's checksum[: {error}]"),
	"credprovider.hash.mismatch": {Msg: "The setup-lock screen download is corrupt (checksum mismatch)", Cat: LogCatDeployment, Hint: hintHash},
	"credprovider.hash.ok":       e(LogCatDeployment, "Setup-lock screen download verified"),
	"credprovider.hash.skipped":  e(LogCatDeployment, "Setup-lock screen download not verified[ ({reason})]"),
	"credprovider.fetched":       eOK(LogCatDeployment, "Setup-lock screen downloaded"),
	"brand.fetch.skip":           e(LogCatSystem, "No branding applied[ ({reason})]"),
	"brand.oem.applied":          eOK(LogCatSystem, "OEM branding applied"),
	"brand.oem.write.fail":       e(LogCatSystem, "Couldn't write OEM branding[ {key}][: {error}]"),
	"lock.brand.skip":            e(LogCatSystem, "Setup-lock screen uses default branding[ ({reason})]"),
	"lock.pin.respond":           e(LogCatSecurity, "Couldn't answer the setup-lock PIN check[: {error}]"),
	"lock.pin.checked":           e(LogCatSecurity, "Setup-lock PIN checked[: {result}]"),
	"logs.ship.ok":               e(LogCatSystem, "Sent[ {events}] log events to the server"),
	"logs.ship.fail":             e(LogCatSystem, "Couldn't send logs to the server[: {error}]"),
	"logs.ship.periodic.fail":    e(LogCatSystem, "Couldn't send logs to the server (will retry)[: {error}]"),
	"logs.buffer.dropped":        {Msg: "[{dropped} ]older log lines were dropped because the client's log buffer was full", Cat: LogCatSystem, Level: "WARN", Hint: "Earlier detail for this run is missing; the warnings and errors were kept."},

	// ---- Agent ----------------------------------------------------------
	"agent.start":               e(LogCatDeployment, "Agent started[ ({os}/{arch})]"),
	"agent.idle":                e(LogCatSystem, "Agent idle[ ({reason})]"),
	"agent.done":                e(LogCatDeployment, "Agent finished the deployment[ ({outcome})]"),
	"service.install":           eFail(LogCatSystem, "Couldn't install the agent service[: {error}]", "Run the agent installer as an administrator."),
	"service.install.ok":        eOK(LogCatSystem, "Agent service installed"),
	"service.uninstall":         eFail(LogCatSystem, "Couldn't remove the agent service[: {error}]", "Run the uninstall as an administrator."),
	"service.uninstall.ok":      eOK(LogCatSystem, "Agent service removed"),
	"service.run":               eFail(LogCatSystem, "The agent service stopped with an error[: {error}]", "Check the agent's local log file on the machine."),
	"software.fetch":            eFail(LogCatSoftware, "Couldn't fetch the software list from the server[: {error}]", hintNetwork),
	"software.warning":          e(LogCatSoftware, "{message} || Software list warning"),
	"software_push.warning":     e(LogCatSoftware, "{message} || Software push warning"),
	"report.open":               e(LogCatDeployment, "Couldn't open the deployment report[: {error}]"),
	"report.close":              e(LogCatDeployment, "Couldn't close the deployment report[: {error}]"),
	"enroll.fetch":              e(LogCatSystem, "Couldn't enrol with the server[: {error}]"),
	"enroll.empty":              e(LogCatSystem, "The server didn't return an agent ID"),
	"enroll.persist":            e(LogCatSystem, "Couldn't save the agent ID on the machine[: {error}]"),
	"enroll.ok":                 eOK(LogCatSystem, "Agent enrolled with the server"),
	"self.fetch":                e(LogCatSystem, "Couldn't fetch this machine's settings from the server[: {error}]"),
	"self.warning":              e(LogCatSystem, "{message} || Settings warning from the server"),
	"rename.desired":            e(LogCatAdmin, "Renaming the computer[ from {from}] to {to}"),
	"rename.failed":             eFail(LogCatAdmin, "Couldn't rename the computer[ (exit code {exit_code})][: {error}]", "Check the name is valid (max 15 characters, no spaces) and not already in use in the domain."),
	"setuplock.reboot":          e(LogCatDeployment, "Restarting to remove the setup-lock screen[ ({note})][: {error}]"),
	"setuplock.reboot.skip":     e(LogCatDeployment, "Not restarting (dry run)"),
	"hardware.report":           e(LogCatSystem, "Couldn't send the hardware report[: {error}]"),
	"hardware.reported":         e(LogCatSystem, "Hardware report sent"),
	"identity.report":           e(LogCatSystem, "Couldn't send the machine identity[: {error}]"),
	"checkin.interval":          e(LogCatSystem, "Check-in interval changed[ from {old}][ to {new}]"),
	"checkin.fetch":             e(LogCatSystem, "Couldn't check in with the server[: {error}]"),
	"bulk.start":                e(LogCatAdmin, "Starting a queued job[: {action}]"),
	"selfupdate.available":      e(LogCatSystem, "Agent update available[ ({from} → {to})]"),
	"selfupdate.checkfail":      e(LogCatSystem, "Couldn't check for an agent update[: {error}]"),
	"selfupdate.path":           e(LogCatSystem, "Couldn't find the agent's own program file[: {error}]"),
	"selfupdate.download":       e(LogCatSystem, "Couldn't download the agent update[: {error}]"),
	"selfupdate.downloaded":     e(LogCatSystem, "Agent update downloaded"),
	"selfupdate.swap":           e(LogCatSystem, "Couldn't start the agent update[: {error}]"),
	"selfupdate.exiting":        e(LogCatSystem, "Agent restarting to finish its update"),
	"domainjoin.skip":           e(LogCatDomain, "Not joining a domain[ ({reason})]"),
	"domainjoin.fetch":          e(LogCatDomain, "Couldn't fetch the domain-join settings[: {error}]"),
	"domainjoin.response":       e(LogCatDomain, "Domain-join settings received[ (domain {domain})]"),
	"domainjoin.already_member": e(LogCatDomain, "Already a member of[ {current_domain}] the domain"),
	"domainjoin.fail":           eFail(LogCatDomain, "Couldn't join[ {domain}] the domain[: {error}]", "Check the join account's password and rights, that the target OU exists, and that the machine can reach a domain controller (DNS)."),
	"domainjoin.ok":             eOK(LogCatDomain, "Joined the domain[ {domain}][ in {ou}]"),
	"detect.error":              e(LogCatSoftware, "Couldn't check whether[ {package}] is installed[: {error}]"),
	"detect.file.not_detected":  e(LogCatSoftware, "Detection rule: file not found"),
	"package.skip":              e(LogCatSoftware, "Skipped {target}[ ({reason})]"),
	"package.no_detection":      {Msg: "{package} has no detection rule; it will be installed every time || A package has no detection rule", Cat: LogCatSoftware, Hint: "Add a detection rule to the package so the agent can tell when it is already installed."},
	"package.workdir":           eFail(LogCatSoftware, "Couldn't create a working folder for {package}[: {error}] || Couldn't create a package working folder[: {error}]", "Check free disk space on the machine."),
	"package.download.mkdir":    eFail(LogCatSoftware, "Couldn't create a download folder for {package}[: {error}] || Couldn't create a download folder[: {error}]", "Check free disk space on the machine."),
	"package.download.create":   eFail(LogCatSoftware, "Couldn't save the download for {package}[: {error}] || Couldn't save a package download[: {error}]", "Check free disk space on the machine."),
	"package.download":          eFail(LogCatSoftware, "Couldn't download {package}[: {error}] || Couldn't download a package[: {error}]", hintNetwork),
	"package.download.ok":       e(LogCatSoftware, "Downloaded {package}[ ({file})] || Package downloaded"),
	"package.bundle.create":     eFail(LogCatSoftware, "Couldn't save the files bundle[ {bundle}] for {package}[: {error}] || Couldn't save a files bundle[: {error}]", "Check free disk space on the machine."),
	"package.bundle.download":   eFail(LogCatSoftware, "Couldn't download the files bundle[ {bundle}] for {package}[: {error}] || Couldn't download a files bundle[: {error}]", hintNetwork),
	"package.bundle.extract":    eFail(LogCatSoftware, "Couldn't unpack the files bundle[ {bundle}] for {package}[: {error}] || Couldn't unpack a files bundle[: {error}]", "Re-upload the bundle in the portal; the zip may be damaged."),
	"package.bundle.ok":         e(LogCatSoftware, "Files bundle[ {bundle}] ready for {package} || Files bundle ready"),
	"package.install.start":     e(LogCatSoftware, "Installing {target}[ ({steps} steps)] || Installing a package"),
	"package.install.ok":        eOK(LogCatSoftware, "Installed {target} || Package installed"),
	"package.install.fail":      {Msg: "{target} failed to install[ at step {step} of {steps}][ — exit code {exit_code}][ ({exit_meaning})] || A package failed to install", Cat: LogCatSoftware, HintFn: packageFailHint, Outcome: "failed"},
	"steps.exec":                e(LogCatSoftware, "Running {target}[ {args}]"),
	"steps.script":              e(LogCatSoftware, "Running a[ {shell}] script"),
	"steps.unzip":               e(LogCatSoftware, "Unzipping[ {source}][ to {destination}]"),
	"steps.copy":                e(LogCatSoftware, "Copying[ {source}][ to {destination}]"),
	"steps.exec.stdout":         {Msg: "{line}", Cat: LogCatSoftware, Level: "INFO"},
	"steps.exec.stderr":         {Msg: "{line}", Cat: LogCatSoftware, Level: "INFO"},

	// ---- Agent: Windows Updates ----------------------------------------
	"update.job.start":        e(LogCatUpdates, "Installing update[ {kb}] || Installing an update"),
	"update.download.create":  eFail(LogCatUpdates, "Couldn't save the update download[: {error}]", "Check free disk space on the machine."),
	"update.download":         eFail(LogCatUpdates, "Couldn't download the update[: {error}]", hintNetwork),
	"update.install.run":      eFail(LogCatUpdates, "Couldn't start the update installer[: {error}]", "Check the Windows Update service (wuauserv) is running on the machine."),
	"update.install.fail":     {Msg: "Update {target} failed to install[ — exit code {exit_code}][ ({reason})] || An update failed to install[ — exit code {exit_code}][ ({reason})]", Cat: LogCatUpdates, Outcome: "failed", Hint: "Check the update applies to this Windows version, and look in the machine's CBS log (C:\\Windows\\Logs\\CBS) for the reason."},
	"update.install.ok":       eOK(LogCatUpdates, "Update {target} installed[ ({reason})] || Update installed[ ({reason})]"),
	"update.reboot":           e(LogCatUpdates, "Couldn't schedule the restart after updates[: {error}]"),
	"update.reboot.scheduled": e(LogCatUpdates, "Restart scheduled to finish installing updates[ (in {delay_seconds}s)]"),
	"update.reboot.skip":      e(LogCatUpdates, "Not restarting after updates (dry run)"),
	"kbscan.fail":             e(LogCatUpdates, "Couldn't list the installed updates[: {error}]"),
	"kbscan.report":           e(LogCatUpdates, "Couldn't report installed updates to the server[: {error}]"),
	"kbscan.reported":         e(LogCatUpdates, "Reported[ {count}] installed updates"),
	"update.jobs.claimed":     e(LogCatUpdates, "Picked up update jobs[ ({kbs})]"),
	"update.job.result":       e(LogCatUpdates, "Update[ {kb}] finished: {status} || Update job finished"),

	// ---- Server: deployments, audit ------------------------------------
	"deploy.started":               e(LogCatDeployment, "Deployment started[ with image {image}]"),
	"deploy.reimage":               e(LogCatDeployment, "Re-image started[ with image {image}][ (by {source})]"),
	"deploy.staged":                e(LogCatDeployment, "Windows install media staged; restarting into Windows Setup"),
	"deploy.setup":                 e(LogCatDeployment, "Windows Setup reached {phase} || Windows Setup progress"),
	"deploy.finished":              eOK(LogCatDeployment, "Deployment finished[ — {notes}]"),
	"deploy.failed":                {Msg: "Deployment failed[: {notes}]", Cat: LogCatDeployment, Level: "ERROR", Outcome: "failed"},
	"auth.login.ok":                eOK(LogCatSecurity, "{user} signed in[ from {ip}]"),
	"auth.login.fail":              {Msg: "Failed sign-in for “{username}”[ from {ip}]", Cat: LogCatSecurity, Level: "WARN", Outcome: "failed", Hint: "Repeated failures from one address may be a password-guessing attempt."},
	"auth.logout":                  e(LogCatSecurity, "{user} signed out"),
	"auth.bootstrap_admin":         e(LogCatSecurity, "Created the first admin account (password written to admin-bootstrap.txt)"),
	"secret.access":                e(LogCatSecurity, "The {secret} for {target} was handed to the {by} (value not logged) || A secret was read[: {target}][ by {by}] (value not logged)"),
	"machine.binding.update":       e(LogCatAdmin, "{user} changed the binding of {target}"),
	"machine.reimage.request":      {Msg: "{user} requested a re-image of {target}", Cat: LogCatAdmin, Level: "WARN"},
	"machine.reimage.cancel":       e(LogCatAdmin, "{user} cancelled the pending re-image of {target}"),
	"machine.action":               e(LogCatAdmin, "{user} queued “{action}” on {target}"),
	"machine.delete":               {Msg: "{user} deleted machine {name} || {user} deleted machine {target}", Cat: LogCatAdmin, Level: "WARN"},
	"settings.update":              e(LogCatAdmin, "{user} changed the {section} settings || {user} changed settings"),
	"bulk.create":                  e(LogCatAdmin, "{user} created bulk operation {target}"),
	"bulk.clear":                   e(LogCatAdmin, "{user} cleared the completed bulk operations"),
	"machine.bulk_delete":          {Msg: "{user} deleted[ {count}] machines from inventory", Cat: LogCatAdmin, Level: "WARN"},
	"machine.group_membership":     e(LogCatAdmin, "{user} changed the group membership of[ {count}] machines"),
	"update_deployment.create":     e(LogCatUpdates, "{user} started a Windows update deployment[ ({count} updates)]"),
	"portal.request.failed":        e(LogCatSystem, "A portal request failed[: {error}]"),
	"package.step":                 e(LogCatSoftware, "{package}: step {step}[ ({type})] finished[ — exit code {exit_code}][ ({exit_meaning})][: {error}] || A package step finished"),
	"server.start":                 e(LogCatSystem, "Server started[ (version {version})]"),
	"http.listen":                  e(LogCatSystem, "Listening for HTTP[ on {addr}]"),
	"https.listen":                 e(LogCatSystem, "Listening for HTTPS[ on {addr}]"),
	"http.cleartext_public_bind":   e(LogCatSecurity, "Serving plain HTTP on a public address"),
	"http.request":                 e(LogCatSystem, "HTTP request[ {method}][ {target}][ → {status}]"),
	"tls.self_signed_generated":    e(LogCatSecurity, "Generated a self-signed TLS certificate"),
	"retention.scheduler_started":  e(LogCatSystem, "Log retention scheduler started"),
	"bulksched.scheduler_started":  e(LogCatAdmin, "Bulk operation scheduler started"),
	"binding.bulk_edit":            e(LogCatAdmin, "{user} bulk-edited[ {machines}] machine bindings[ ({updated} changed)] || Machine bindings bulk-edited"),
	"asset.import":                 e(LogCatAdmin, "{user} imported assets[ from {file}][ ({inserted} new, {updated} updated)] || Assets imported"),
	"asset.wipe":                   {Msg: "{user} wiped[ {removed}] imported assets || Imported assets wiped", Cat: LogCatAdmin, Level: "WARN"},
	"group.create":                 e(LogCatAdmin, "{user} created machine group {target}"),
	"group.update":                 e(LogCatAdmin, "{user} updated machine group {target}"),
	"group.delete":                 {Msg: "{user} deleted machine group {target}", Cat: LogCatAdmin, Level: "WARN"},
	"software.upload.bad_request":  eFail(LogCatSoftware, "Software upload rejected: the request was malformed[: {error}]", "Retry the upload from the portal."),
	"software.upload.stream_error": eFail(LogCatSoftware, "Software upload was interrupted[: {error}]", "Retry the upload; check the connection to the server."),
	"software.upload.rejected":     eFail(LogCatSoftware, "Software upload refused[: {error}]", "Check the file name and size limits."),
	"software.upload.write_failed": eFail(LogCatSoftware, "Couldn't save an uploaded software file[: {error}]", "Check free space on the server's data disk."),
	"software.upload.file_ok":      eOK(LogCatSoftware, "Uploaded[ {file}] to {target} || Software file uploaded"),
	"software.upload.empty":        e(LogCatSoftware, "Software upload contained no files"),
	"software.bundle.bad_request":  eFail(LogCatSoftware, "Files-bundle upload rejected: the request was malformed[: {error}]", "Retry the upload from the portal."),
	"software.bundle.stream_error": eFail(LogCatSoftware, "Files-bundle upload was interrupted[: {error}]", "Retry the upload; check the connection to the server."),
	"software.bundle.rejected":     eFail(LogCatSoftware, "Files-bundle upload refused[: {error}]", "Check the file name and size limits."),
	"software.bundle.write_failed": eFail(LogCatSoftware, "Couldn't save an uploaded files bundle[: {error}]", "Check free space on the server's data disk."),
	"software.bundle.file_ok":      eOK(LogCatSoftware, "Uploaded files bundle[ {file}] to {target} || Files bundle uploaded"),
	"software.bundle.empty":        e(LogCatSoftware, "Files-bundle upload contained no files"),

	// ---- Server: background jobs (stdout only; listed so they read well if
	// ever stored) --------------------------------------------------------
	"addomain.configured":                   e(LogCatDomain, "Active Directory connection configured"),
	"addomain.groups.partial":               e(LogCatDomain, "Some AD group memberships couldn't be applied[: {error}]"),
	"addomain.prepare.start":                e(LogCatDomain, "Preparing the AD computer object[ {target}]"),
	"addomain.prepare.delete_existing":      e(LogCatDomain, "Removing the existing AD computer object[ {target}]"),
	"addomain.prepare.ok":                   eOK(LogCatDomain, "AD computer object ready[ {target}]"),
	"addomain.rename.start":                 e(LogCatDomain, "Renaming the AD computer object[ {target}]"),
	"addomain.rename.skip":                  e(LogCatDomain, "AD rename skipped[ ({reason})]"),
	"addomain.rename.ok":                    eOK(LogCatDomain, "AD computer object renamed[ {target}]"),
	"bulk.rename.ad.ok":                     eOK(LogCatDomain, "AD object renamed for a bulk rename[ {target}]"),
	"bulk.rename.ad.fail":                   eFail(LogCatDomain, "Couldn't rename the AD object for a bulk rename[: {error}]", "Check the AD connection settings and the service account's rights."),
	"bulksched.run.ok":                      e(LogCatAdmin, "Scheduled bulk operation started"),
	"bulksched.run.fail":                    e(LogCatAdmin, "Couldn't start a scheduled bulk operation[: {error}]"),
	"bulksched.advance.fail":                e(LogCatAdmin, "Couldn't advance a recurring bulk operation[: {error}]"),
	"bulksched.expire.ok":                   e(LogCatAdmin, "Expired bulk operations closed"),
	"bulksched.expire.fail":                 e(LogCatAdmin, "Couldn't close expired bulk operations[: {error}]"),
	"bulksched.list_due.fail":               e(LogCatAdmin, "Couldn't list scheduled bulk operations[: {error}]"),
	"isobuild.exec":                         e(LogCatSystem, "Building an ISO[ ({args})]"),
	"isobuild.exec.stdout":                  {Msg: "{line}", Cat: LogCatSystem, Level: "INFO"},
	"isobuild.exec.stderr":                  {Msg: "{line}", Cat: LogCatSystem, Level: "INFO"},
	"notify.offline.emitted":                e(LogCatSystem, "Offline-machine alert sent"),
	"notify.offline.list.fail":              e(LogCatSystem, "Couldn't list offline machines[: {error}]"),
	"notify.offline.mark.fail":              e(LogCatSystem, "Couldn't record an offline alert[: {error}]"),
	"notify.offline.names.fail":             e(LogCatSystem, "Couldn't load machine names for an offline alert[: {error}]"),
	"notify.webhook.list_failed":            e(LogCatSystem, "Couldn't list webhooks[: {error}]"),
	"notify.webhook.record_failed":          e(LogCatSystem, "Couldn't record a webhook delivery[: {error}]"),
	"portal.delete.failed":                  e(LogCatAdmin, "A delete failed[: {error}]"),
	"portal.render.failed":                  e(LogCatSystem, "A portal page failed to render[: {error}]"),
	"retention.log_prune.ok":                e(LogCatSystem, "Old log events pruned"),
	"retention.log_prune.fail":              e(LogCatSystem, "Couldn't prune old log events[: {error}]"),
	"retention.notification_prune.ok":       e(LogCatSystem, "Old notifications pruned"),
	"retention.notification_prune.fail":     e(LogCatSystem, "Couldn't prune old notifications[: {error}]"),
	"retention.session_prune.fail":          e(LogCatSystem, "Couldn't prune expired sessions[: {error}]"),
	"retention.webhook_delivery_prune.ok":   e(LogCatSystem, "Old webhook deliveries pruned"),
	"retention.webhook_delivery_prune.fail": e(LogCatSystem, "Couldn't prune old webhook deliveries[: {error}]"),
	"retention.imported_asset_sweep.ok":     e(LogCatSystem, "Stale imported assets swept"),
	"retention.imported_asset_sweep.fail":   e(LogCatSystem, "Couldn't sweep stale imported assets[: {error}]"),
	"server.version":                        e(LogCatSystem, "Server started[ (version {version})]"),
	"server.fatal":                          e(LogCatSystem, "Server stopped with a fatal error[: {error}]"),
	"service.unexpected_control":            e(LogCatSystem, "Server service received an unexpected control request"),
	"storage.override.failed":               e(LogCatSystem, "Couldn't apply a storage override[: {error}]"),
	"tftp.listen":                           e(LogCatSystem, "TFTP server listening"),
	"wol.sent":                              e(LogCatAdmin, "Wake-on-LAN packet sent[ to {target}]"),
	"wol.no_macs":                           e(LogCatAdmin, "Can't wake[ {target}]: no MAC address known"),
	"wol.addr.fail":                         e(LogCatAdmin, "Couldn't resolve a Wake-on-LAN address[: {error}]"),
	"wol.machine.fail":                      e(LogCatAdmin, "Couldn't wake a machine[: {error}]"),
	"wol.send.fail":                         e(LogCatAdmin, "Couldn't send a Wake-on-LAN packet[: {error}]"),
	"wol.socket.fail":                       e(LogCatAdmin, "Couldn't open the Wake-on-LAN socket[: {error}]"),
}

// AuditNouns names each kind of portal object the audit trail records
// create/update/delete (and a few other verbs) for. init() turns it into
// catalogue entries such as "image.create" → "admin created image Lab".
var AuditNouns = map[string]string{
	"image": "image", "iso": "ISO", "unattend": "answer file", "driver": "driver package",
	"software": "software package", "loadout": "loadout", "mirror": "payload mirror",
	"webhook": "webhook", "account": "user account", "update": "Windows update",
	"image_group": "image group", "bulk": "bulk operation", "update_deployment": "update deployment",
}

// auditVerbs are the past-tense verbs for audited actions.
var auditVerbs = map[string]string{
	"create": "created", "update": "updated", "delete": "deleted", "clone": "cloned",
	"upload": "uploaded files to", "extract": "started extracting", "cancel": "cancelled",
	"pause": "paused", "resume": "resumed", "run_now": "started", "enable": "enabled",
	"disable": "disabled", "password": "reset the password of", "test": "tested",
	"file_delete": "deleted a file from",
}

func init() {
	for kind, noun := range AuditNouns {
		for verb, past := range auditVerbs {
			a := kind + "." + verb
			if _, exists := logCatalog[a]; exists {
				continue
			}
			article := "a "
			if strings.ContainsRune("aeiouAEIOU", rune(noun[0])) || strings.HasPrefix(noun, "ISO") {
				article = "an "
			}
			en := logEntry{Cat: LogCatAdmin,
				Msg: "{user} " + past + " " + noun + " {target} || {user} " + past + " " + noun + " “{name}” || {user} " + past + " " + article + noun}
			if _, linkable := kindAliases[kind]; !linkable {
				// The target ("account:3") isn't resolvable; lead with the
				// name captured when the event was recorded.
				en.Msg = "{user} " + past + " " + noun + " “{name}” || {user} " + past + " " + article + noun
			}
			if verb == "delete" {
				// The row is gone by the time it is shown, so prefer the
				// name captured before the delete.
				en.Msg = "{user} deleted " + noun + " “{name}” || {user} deleted " + noun + " {target}"
				en.Level = "WARN"
			}
			logCatalog[a] = en
		}
	}
}

// logPrefixCategories maps an unknown action's first segment to a category.
var logPrefixCategories = map[string]string{
	"boot": LogCatDeployment, "deploy": LogCatDeployment, "imaging": LogCatDeployment,
	"manifest": LogCatDeployment, "download": LogCatDeployment, "menu": LogCatDeployment,
	"media": LogCatDeployment, "gui": LogCatDeployment, "reimage": LogCatDeployment,
	"credprovider": LogCatDeployment, "agent": LogCatDeployment,
	"package": LogCatSoftware, "software": LogCatSoftware, "steps": LogCatSoftware,
	"detect": LogCatSoftware, "loadout": LogCatSoftware,
	"update": LogCatUpdates, "updates": LogCatUpdates, "kbscan": LogCatUpdates,
	"domainjoin": LogCatDomain, "addomain": LogCatDomain, "ad": LogCatDomain,
	"auth": LogCatSecurity, "secret": LogCatSecurity, "pin": LogCatSecurity, "lock": LogCatSecurity,
	"machine": LogCatAdmin, "image": LogCatAdmin, "iso": LogCatAdmin, "unattend": LogCatAdmin,
	"driver": LogCatAdmin, "group": LogCatAdmin, "bulk": LogCatAdmin, "settings": LogCatAdmin,
	"binding": LogCatAdmin, "asset": LogCatAdmin, "mirror": LogCatAdmin, "webhook": LogCatAdmin,
	"account": LogCatAdmin, "wol": LogCatAdmin, "rename": LogCatAdmin,
}

// logCategoryOf returns an action's category: its catalogue entry's, else by
// its first segment, else System.
func logCategoryOf(action string) string {
	if en, ok := logCatalog[action]; ok && en.Cat != "" {
		return en.Cat
	}
	first := action
	if i := strings.IndexAny(action, "._"); i > 0 {
		first = action[:i]
	}
	if c, ok := logPrefixCategories[first]; ok {
		return c
	}
	return LogCatSystem
}

// HasLogCatalogEntry reports whether action has its own catalogue entry.
func HasLogCatalogEntry(action string) bool {
	_, ok := logCatalog[action]
	return ok
}

// LogDescription is what the catalogue says about one event.
type LogDescription struct {
	Message  string
	Category string
	Level    string // effective level (catalogue override applied)
	Hint     string
	Outcome  string
	Error    string
}

// LogNames carries the resolved display names a message may mention.
type LogNames struct {
	Machine string
	Target  string // the target's display name ("" → the raw target)
}

// DescribeLog renders an event through the catalogue: a plain-English
// message, its category, effective level, hint and outcome. Never fails —
// an unknown action gets a humanised version of its code.
func DescribeLog(ev LogEvent, names LogNames) LogDescription {
	f := parseLogFields(ev.Fields)
	en, known := logCatalog[ev.Action]
	d := LogDescription{Category: logCategoryOf(ev.Action), Level: normLevel(ev.Level), Error: logFieldError(f)}
	if known && en.Level != "" {
		d.Level = en.Level
	}
	vars := logVars{f: f, ev: ev, names: names, err: d.Error}
	if known {
		d.Message = renderLogTemplate(en.Msg, vars)
	}
	if d.Message == "" {
		if m := fieldString(f, "message"); m != "" {
			d.Message = m
		} else {
			d.Message = humaniseAction(ev.Action, vars.target())
		}
	}
	if known {
		d.Hint = en.Hint
		if en.HintFn != nil {
			d.Hint = en.HintFn(f)
		}
		d.Outcome = en.Outcome
	}
	if d.Outcome == "" {
		d.Outcome = deriveOutcome(ev.Action, d.Level, f)
	}
	// A failure-level event from a step that eventually succeeded (exit 3010
	// etc.) is not a failure; trust an explicit ok outcome field.
	if o := strings.ToLower(fieldString(f, "outcome")); o == "ok" || o == "failed" {
		d.Outcome = o
	}
	return d
}

func normLevel(l string) string {
	switch strings.ToUpper(strings.TrimSpace(l)) {
	case "ERROR", "ERR":
		return "ERROR"
	case "WARN", "WARNING":
		return "WARN"
	case "DEBUG":
		return "DEBUG"
	}
	return "INFO"
}

// deriveOutcome guesses ok/failed for events without a catalogue outcome.
func deriveOutcome(action, level string, f map[string]any) string {
	if level == "ERROR" {
		return "failed"
	}
	last := action
	if i := strings.LastIndexByte(action, '.'); i >= 0 {
		last = action[i+1:]
	}
	switch last {
	case "ok", "done", "success", "succeeded":
		return "ok"
	case "fail", "failed", "error":
		return "failed"
	}
	return ""
}

// humaniseAction turns "deploy.step_failed" into "Deploy step failed",
// appending the target when there is one.
func humaniseAction(action, target string) string {
	s := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(strings.TrimSpace(action))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		s = "Event"
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if target != "" {
		s += " (" + target + ")"
	}
	return s
}

// logVars resolves template placeholders for one event.
type logVars struct {
	f     map[string]any
	ev    LogEvent
	names LogNames
	err   string
}

func (v logVars) target() string {
	if v.names.Target != "" {
		return v.names.Target
	}
	return strings.TrimSpace(v.ev.Target)
}

func (v logVars) get(name string) string {
	switch name {
	case "target":
		return v.target()
	case "machine":
		return v.names.Machine
	case "user":
		if v.ev.User != "" {
			return v.ev.User
		}
		return strings.TrimSpace(v.ev.Actor)
	case "error":
		return v.err
	case "exit_meaning":
		if s := fieldString(v.f, "exit_meaning"); s != "" {
			return s
		}
		if code, ok := fieldInt(v.f, "exit_code"); ok {
			return MSIExitMeaning(code)
		}
		return ""
	case "message":
		return fieldString(v.f, "message")
	}
	return fieldString(v.f, name)
}

// renderLogTemplate expands a catalogue template (see logEntry).
func renderLogTemplate(tmpl string, v logVars) string {
	for _, alt := range strings.Split(tmpl, "||") {
		if s, ok := renderLogAlt(strings.TrimSpace(alt), v); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// renderLogAlt renders one alternative; ok is false when a placeholder
// outside an optional [ ] segment has no value.
func renderLogAlt(t string, v logVars) (string, bool) {
	var b strings.Builder
	for len(t) > 0 {
		switch t[0] {
		case '[':
			end := strings.IndexByte(t, ']')
			if end < 0 {
				end = len(t)
			}
			if seg, ok := renderLogPlain(t[1:end], v); ok {
				b.WriteString(seg)
			}
			if end == len(t) {
				t = ""
			} else {
				t = t[end+1:]
			}
		default:
			next := strings.IndexByte(t, '[')
			if next < 0 {
				next = len(t)
			}
			seg, ok := renderLogPlain(t[:next], v)
			if !ok {
				return "", false
			}
			b.WriteString(seg)
			t = t[next:]
		}
	}
	return b.String(), true
}

// renderLogPlain substitutes {name} placeholders; ok is false if any is empty.
func renderLogPlain(t string, v logVars) (string, bool) {
	var b strings.Builder
	ok := true
	for {
		i := strings.IndexByte(t, '{')
		if i < 0 {
			b.WriteString(t)
			break
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			b.WriteString(t)
			break
		}
		b.WriteString(t[:i])
		val := v.get(t[i+1 : i+j])
		if val == "" {
			ok = false
		}
		b.WriteString(val)
		t = t[i+j+1:]
	}
	return b.String(), ok
}

// parseLogFields decodes a row's fields JSON (nil map on bad JSON).
func parseLogFields(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// ParseLogFields is parseLogFields for callers outside the package.
func ParseLogFields(s string) map[string]any { return parseLogFields(s) }

// fieldString renders one field as text: strings as-is, whole numbers
// without decimals, lists comma-joined. Objects (older agents shipped Go
// errors as {}) and nulls are empty.
func fieldString(f map[string]any, k string) string {
	if f == nil {
		return ""
	}
	return LogFieldText(f[k])
}

// LogFieldText formats one decoded JSON field value for display.
func LogFieldText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			if s := LogFieldText(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if len(x) == 0 {
			return ""
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if s := LogFieldText(x[k]); s != "" {
				parts = append(parts, k+"="+s)
			}
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

func fieldInt(f map[string]any, k string) (int, bool) {
	if f == nil {
		return 0, false
	}
	switch x := f[k].(type) {
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// logFieldError is the event's error text: fields.error, else fields.reason
// for failure events that only carry that.
func logFieldError(f map[string]any) string {
	return fieldString(f, "error")
}

// LogActionsMatchingText returns the catalogued actions whose message
// template (placeholders removed) contains q, so a free-text search for
// "failed to install" finds rows whose message is only rendered at read time.
func LogActionsMatchingText(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var out []string
	for action, en := range logCatalog {
		static := strings.ToLower(stripPlaceholders(en.Msg))
		if strings.Contains(static, q) {
			out = append(out, action)
		}
	}
	sort.Strings(out)
	return out
}

func stripPlaceholders(t string) string {
	var b strings.Builder
	depth := 0
	for _, r := range t {
		switch r {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '[', ']', '|':
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// logCategoryActions returns, for category cat, the catalogued actions in it
// and the fallback prefixes ("deploy.") that map to it.
func logCategoryActions(cat string) (exact []string, prefixes []string) {
	for a, en := range logCatalog {
		if en.Cat == cat {
			exact = append(exact, a)
		}
	}
	for p, c := range logPrefixCategories {
		if c == cat {
			prefixes = append(prefixes, p)
		}
	}
	sort.Strings(exact)
	sort.Strings(prefixes)
	return exact, prefixes
}

// logLevelOverrides returns the catalogued actions whose level is overridden,
// grouped by the level they are shown at.
func logLevelOverrides() map[string][]string {
	out := map[string][]string{}
	for a, en := range logCatalog {
		if en.Level != "" {
			out[en.Level] = append(out[en.Level], a)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// LogOutputAction reports whether action is one line of a command's output
// (steps.exec.stdout, imaging.exec.stderr, …) — the Logs page folds runs of
// these into one collapsible block.
func LogOutputAction(action string) bool {
	switch action {
	case "steps.exec.stdout", "steps.exec.stderr", "imaging.exec.stdout",
		"imaging.exec.stderr", "imaging.exec.output", "isobuild.exec.stdout",
		"isobuild.exec.stderr", "net.usbnic.dmesg":
		return true
	}
	return false
}

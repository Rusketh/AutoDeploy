package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/boot-client/internal/httpc"
	"github.com/rusketh/autodeploy/boot-client/internal/imaging"
	"github.com/rusketh/autodeploy/boot-client/internal/logging"
	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
	"github.com/rusketh/autodeploy/boot-client/internal/ui"
)

// machineName is the computer name the server sent with the menu (empty
// when the server doesn't send one), shown in the imaging screens' header.
var machineName string

// deployStarted is when the current deploy began, for the elapsed timers.
var deployStarted time.Time

// deployFailure describes one way a run can fail, in the terms each
// audience needs: Action keeps the existing log action name (so existing
// server/UI matching still works), Code is a short searchable code for
// the screen and logs, What is the plain-English explanation a technician
// can act on, and Err is the low-level cause appended to it.
type deployFailure struct {
	Action string
	Code   string
	Title  string // screen headline; empty = "Deployment failed"
	What   string
	Err    error
	// Output is the failing command's last stderr lines, when the failure
	// came from an external tool.
	Output []string
	// Attrs are extra slog attrs for the failure event (e.g. the URL).
	Attrs []any
	// Exit is the process exit code if the technician leaves to the shell.
	Exit int
}

// message is What with the low-level cause appended:
// "Couldn't prepare the disk (partitioning failed): zap /dev/sda: exit status 2".
func (d deployFailure) message() string {
	if d.Err == nil {
		return d.What
	}
	return d.What + ": " + d.Err.Error()
}

// output is the stderr tail to show, but only when the failure really was
// an external command exiting non-zero -- otherwise the runner's last
// output belongs to some earlier, successful command and would mislead.
func (d deployFailure) output() []string {
	var ee *exec.ExitError
	if len(d.Output) == 0 || !errors.As(d.Err, &ee) {
		return nil
	}
	return d.Output
}

// failDeploy ends the run on a failure. It never returns. In order it:
// logs the failure (existing action name, plus error_code / message /
// stderr_tail fields); reports the deployment "failed" with the
// plain-English message (when c is non-nil and an image is known);
// ships the buffered logs, so the reason reaches the portal before
// anything else can go wrong; shows the failure screen (framebuffer) or
// a failure block (console); then acts on the technician's choice: retry
// (start the boot client over), restart the machine, or exit to the
// initramfs shell with d.Exit.
func failDeploy(ctx context.Context, log *slog.Logger, f bootFlags, c *httpc.Client, id smbios.Identity, imageID int64, shipper *logging.Shipper, d deployFailure) {
	msg := d.message()
	out := d.output()
	attrs := []any{
		slog.String("error", errString(d.Err)),
		slog.String("error_code", d.Code),
		slog.String("message", msg),
	}
	if len(out) > 0 {
		attrs = append(attrs, slog.String("stderr_tail", strings.Join(out, "\n")))
	}
	attrs = append(attrs, d.Attrs...)
	log.Error(d.Action, attrs...)
	if c != nil && imageID != 0 {
		reportDeployStatus(ctx, c, log, id, imageID, "failed", msg)
	}
	shipped := shipLogs(log, shipper, f.server, f.insecureTLS)

	info := ui.FailureInfo{
		Title:       d.Title,
		Message:     msg,
		Code:        d.Code,
		Identity:    screenIdentity(id, f.server),
		Output:      out,
		LogsShipped: shipped,
	}
	if !deployStarted.IsZero() {
		info.Elapsed = time.Since(deployStarted)
	}
	var choice ui.FailureChoice
	if activeProgress != nil {
		choice = activeProgress.Fail(info)
	} else {
		choice = showFailureStandalone(log, info)
	}
	log.Info("deploy.fail.choice", slog.String("choice", failureChoiceName(choice)))
	switch choice {
	case ui.FailureRetry:
		shipLogs(log, shipper, f.server, f.insecureTLS)
		restartSelf(log)
	case ui.FailureReboot:
		shipLogs(log, shipper, f.server, f.insecureTLS)
		runner := &imaging.OSRunner{Log: log}
		if err := runner.Exec(context.Background(), "reboot", "-f"); err != nil {
			log.Error("reboot", slog.String("error", err.Error()))
		}
	}
	shipLogs(log, shipper, f.server, f.insecureTLS)
	os.Exit(d.Exit)
}

func failureChoiceName(c ui.FailureChoice) string {
	switch c {
	case ui.FailureRetry:
		return "retry"
	case ui.FailureReboot:
		return "reboot"
	case ui.FailureShell:
		return "shell"
	}
	return "none"
}

// printFailure is the text-console form of the failure screen, for runs
// with no framebuffer (or with the progress UI turned off).
func printFailure(info ui.FailureInfo) {
	title := info.Title
	if title == "" {
		title = "Deployment failed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== AutoDeploy: %s ===\n", title)
	fmt.Fprintf(&b, "%s\n", info.Message)
	if info.Code != "" {
		fmt.Fprintf(&b, "Error code: %s\n", info.Code)
	}
	idParts := []string{}
	for _, p := range []struct{ k, v string }{
		{"Machine", info.Identity.Name}, {"Model", info.Identity.Model},
		{"S/N", info.Identity.Serial}, {"IP", info.Identity.IP}, {"Server", info.Identity.Server},
	} {
		if p.v != "" {
			idParts = append(idParts, p.k+" "+p.v)
		}
	}
	if len(idParts) > 0 {
		fmt.Fprintf(&b, "%s\n", strings.Join(idParts, " · "))
	}
	if len(info.Output) > 0 {
		b.WriteString("Last output from the failing command:\n")
		for _, l := range info.Output {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	if info.LogsShipped {
		b.WriteString("Logs sent to the server: see Machine › Deployment in the AutoDeploy portal.\n")
	} else {
		b.WriteString("Couldn't send the logs to the server (is the network up?).\n")
	}
	b.WriteString("Reboot to retry.\n")
	fmt.Print(b.String())
}

// screenIdentity is what the imaging screens print about this machine.
func screenIdentity(id smbios.Identity, server string) ui.Identity {
	model := strings.TrimSpace(id.SystemManufacturer + " " + id.SystemProduct)
	return ui.Identity{
		Name:   machineName,
		Model:  model,
		Serial: id.SystemSerial,
		IP:     firstIPv4(),
		Server: serverHost(server),
	}
}

// serverHost reduces the server URL to host[:port] for display -- the
// screen shows which server the machine talks to, never a path or query.
func serverHost(server string) string {
	if server == "" {
		return ""
	}
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}

// firstIPv4 returns this machine's first non-loopback IPv4 address, or ""
// when it has none (no DHCP lease).
func firstIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() {
			continue
		}
		if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLinkLocalUnicast() {
			return v4.String()
		}
	}
	return ""
}

// deployStages are the progress screen's "Step N of M" steps, in order,
// named exactly as runDeploy reports them via reportStage.
var deployStages = []string{
	"Preparing",
	"Validating media",
	"Preparing disk",
	"Downloading image",
	"Finalising",
	"Ready",
}

// stageStep returns stage's 1-based position in deployStages (0 if it
// isn't one) and the total.
func stageStep(stage string) (int, int) {
	for i, s := range deployStages {
		if s == stage {
			return i + 1, len(deployStages)
		}
	}
	return 0, len(deployStages)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

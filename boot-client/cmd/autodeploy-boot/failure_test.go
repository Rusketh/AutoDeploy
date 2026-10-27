package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/rusketh/autodeploy/boot-client/internal/smbios"
)

func TestDeployFailureMessage(t *testing.T) {
	d := deployFailure{What: "Couldn't prepare the disk (partitioning failed)", Err: errors.New("zap /dev/sda: exit status 2")}
	if got, want := d.message(), "Couldn't prepare the disk (partitioning failed): zap /dev/sda: exit status 2"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if got := (deployFailure{What: "No usable target disk found"}).message(); got != "No usable target disk found" {
		t.Errorf("message without error = %q", got)
	}
}

// The stderr tail is shown only when the failure really was a command
// exiting non-zero (possibly wrapped); otherwise the runner's last output
// belongs to an earlier, successful command.
func TestDeployFailureOutputOnlyForExitErrors(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Skipf("no sh exit error: %v", err)
	}
	tail := []string{"sgdisk: bad"}
	if got := (deployFailure{Err: fmt.Errorf("partition /dev/sda: %w", err), Output: tail}).output(); len(got) != 1 {
		t.Errorf("wrapped exit error should keep output, got %q", got)
	}
	if got := (deployFailure{Err: errors.New("prepare mount point: denied"), Output: tail}).output(); got != nil {
		t.Errorf("non-exec error must drop output, got %q", got)
	}
}

func TestServerHostAndStages(t *testing.T) {
	for in, want := range map[string]string{
		"https://autodeploy.lan:8443/base?x=1": "autodeploy.lan:8443",
		"http://10.0.0.5":                      "10.0.0.5",
		"":                                     "",
	} {
		if got := serverHost(in); got != want {
			t.Errorf("serverHost(%q) = %q, want %q", in, got, want)
		}
	}
	if n, m := stageStep("Downloading image"); n != 4 || m != len(deployStages) {
		t.Errorf("stageStep = %d/%d", n, m)
	}
	if n, _ := stageStep("Something else"); n != 0 {
		t.Errorf("unknown stage = %d", n)
	}
	machineName = "LAB-01"
	defer func() { machineName = "" }()
	id := screenIdentity(smbios.Identity{SystemManufacturer: "Dell Inc.", SystemProduct: "OptiPlex", SystemSerial: "S1"}, "https://srv:8443")
	if id.Name != "LAB-01" || id.Model != "Dell Inc. OptiPlex" || id.Serial != "S1" || id.Server != "srv:8443" {
		t.Errorf("screenIdentity = %+v", id)
	}
}

//go:build windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// cleanupMedia removes the AutoDeploy boot-media partition (label ADBOOT)
// and extends C: into the reclaimed space. This is a SECURITY step as much
// as housekeeping: ADBOOT holds the generated autounattend.xml, which
// contains the local-admin password in plaintext, and it stays readable on
// the installed machine until removed.
//
// The media partition was created at the END of the disk with C: in the
// front free space, so once ADBOOT is gone the freed space is contiguous
// with C: and the resize succeeds. Targets strictly by the ADBOOT label so
// it can never touch C: or the ESP. Returns a short status string.
func cleanupMedia() string {
	const script = `
$ErrorActionPreference='Stop'
$v = Get-Volume -FileSystemLabel 'ADBOOT' -ErrorAction SilentlyContinue
if(-not $v){ Write-Output 'absent'; exit 0 }
$p = Get-Partition -Volume $v
Remove-Partition -DiskNumber $p.DiskNumber -PartitionNumber $p.PartitionNumber -Confirm:$false
try {
  $c = Get-Partition -DriveLetter C
  $max = (Get-PartitionSupportedSize -DiskNumber $c.DiskNumber -PartitionNumber $c.PartitionNumber).SizeMax
  if($max -gt $c.Size){ Resize-Partition -DiskNumber $c.DiskNumber -PartitionNumber $c.PartitionNumber -Size $max }
  Write-Output 'cleaned-and-extended'
} catch {
  Write-Output 'cleaned'
}`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	status := strings.TrimSpace(string(out))
	if err != nil {
		if status == "" {
			status = "error: " + err.Error()
		}
		return "error: " + status
	}
	// PowerShell may emit warnings before the final token; keep the last line.
	if lines := strings.Fields(status); len(lines) > 0 {
		status = lines[len(lines)-1]
	}
	return status
}

package imaging

import (
	"context"
	"strings"
	"testing"
)

func TestApplyIssuesExpectedSteps(t *testing.T) {
	rec := &Recorder{}
	plan := Plan{
		TargetDisk:    "/dev/sda",
		WIMPath:       "/tmp/install.wim",
		WIMImageIndex: 1,
		UnattendPath:  "/tmp/unattend.xml",
		DriverPaths:   []string{"/tmp/drv1", "/tmp/drv2"},
		WorkDir:       "/tmp/work",
	}
	// Use the recorder as the runner so we never touch disk.
	if err := applyDryWith(rec, plan); err != nil {
		t.Fatal(err)
	}
	mustContain := []string{
		"sgdisk --zap-all /dev/sda",
		"sgdisk --new=1:0:+100M --typecode=1:ef00",
		"mkfs.fat -F32 -n ESP /dev/sda1",
		"mkfs.ntfs -Q -L Windows /dev/sda2",
		"mount /dev/sda2 /tmp/work/win",
		"wimlib-imagex apply /tmp/install.wim 1 /tmp/work/win",
		"wimlib-imagex update /tmp/install.wim 1 --command=add /tmp/drv1",
		"wimlib-imagex update /tmp/install.wim 1 --command=add /tmp/drv2",
		"umount /tmp/work/win",
	}
	for _, want := range mustContain {
		if !rec.Has(want) {
			t.Errorf("missing call containing %q\n%s", want, rec.Dump())
		}
	}
}

func TestPartitionNameingNVMe(t *testing.T) {
	if got := partName("/dev/nvme0n1", 2); got != "/dev/nvme0n1p2" {
		t.Errorf("partName(nvme) = %q", got)
	}
	if got := partName("/dev/sda", 2); got != "/dev/sda2" {
		t.Errorf("partName(sda) = %q", got)
	}
}

func TestApplyStopsOnFirstError(t *testing.T) {
	rec := &failAfter{Recorder: &Recorder{}, failAt: 1}
	plan := Plan{TargetDisk: "/dev/sda", WIMPath: "/tmp/install.wim", WorkDir: "/tmp/work"}
	err := Apply(context.Background(), plan, rec)
	if err == nil || !strings.Contains(err.Error(), "partition") {
		t.Fatalf("expected partition error, got %v", err)
	}
}

func applyDryWith(rec *Recorder, plan Plan) error {
	return Apply(context.Background(), plan, rec)
}

// failAfter is a Runner that fails on the Nth call (1-indexed).
type failAfter struct {
	*Recorder
	failAt int
	called int
	failed bool
}

func (f *failAfter) Exec(ctx context.Context, name string, args ...string) error {
	f.called++
	if f.called == f.failAt {
		f.failed = true
		return errSentinel
	}
	return f.Recorder.Exec(ctx, name, args...)
}

type sentinelError string

func (s sentinelError) Error() string { return string(s) }

const errSentinel = sentinelError("forced failure")

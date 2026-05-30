package imaging

import (
	"context"
	"testing"
)

func TestBootMediaBuildSequence(t *testing.T) {
	rec := &Recorder{}
	l := DefaultBootMediaLayout("/dev/sda")
	if err := l.Build(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sgdisk --zap-all /dev/sda",
		// 1 GiB FAT32 ESP + remainder NTFS MEDIA, in one sgdisk call.
		"sgdisk --new=1:0:+1024M --typecode=1:ef00 --change-name=1:ESP --new=2:0:0 --typecode=2:0700 --change-name=2:MEDIA /dev/sda",
		"mkfs.fat -F32 -n ESP /dev/sda1",
		"mkfs.ntfs -Q -L MEDIA /dev/sda2",
	}
	for _, w := range want {
		if !rec.Has(w) {
			t.Errorf("missing call containing %q\n%s", w, rec.Dump())
		}
	}
}

func TestBootMediaBuildNVMePartitionNames(t *testing.T) {
	rec := &Recorder{}
	l := DefaultBootMediaLayout("/dev/nvme0n1")
	if err := l.Build(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	// nvme partitions get the "p" infix.
	if !rec.Has("mkfs.fat -F32 -n ESP /dev/nvme0n1p1") {
		t.Errorf("nvme ESP part name wrong\n%s", rec.Dump())
	}
	if !rec.Has("mkfs.ntfs -Q -L MEDIA /dev/nvme0n1p2") {
		t.Errorf("nvme MEDIA part name wrong\n%s", rec.Dump())
	}
}

func TestRouteFile(t *testing.T) {
	const big = fat32MaxFileBytes + 1
	const small = int64(4096)
	cases := []struct {
		path string
		size int64
		want Target
	}{
		// The headline case: a >4 GiB install.wim must go to MEDIA even
		// though sources/ would send it there anyway -- size wins first.
		{"sources/install.wim", big, TargetMedia},
		// An ESD that happens to be under 4 GiB still goes to MEDIA
		// because it lives under sources/.
		{"sources/install.esd", small, TargetMedia},
		{"sources/boot.wim", small, TargetMedia},
		// Boot files and the EFI loader belong on the FAT32 ESP.
		{"efi/boot/bootx64.efi", small, TargetESP},
		{"boot/bcd", small, TargetESP},
		{"setup.exe", small, TargetESP},
		// Backslash-separated media paths normalise the same way.
		{"sources\\install.wim", big, TargetMedia},
		{"EFI\\Boot\\bootx64.efi", small, TargetESP},
		// A hypothetical >4 GiB file outside sources/ is still forced to
		// MEDIA by the FAT32 limit.
		{"bigfile.bin", big, TargetMedia},
	}
	for _, c := range cases {
		if got := RouteFile(c.path, c.size); got != c.want {
			t.Errorf("RouteFile(%q, %d) = %v, want %v", c.path, c.size, got, c.want)
		}
	}
}

func TestBootMediaMountUnmount(t *testing.T) {
	rec := &Recorder{}
	l := DefaultBootMediaLayout("/dev/sda")
	mp, err := l.Mount(context.Background(), rec, "/run/autodeploy")
	if err != nil {
		t.Fatal(err)
	}
	if mp.ESP != "/run/autodeploy/esp" || mp.Media != "/run/autodeploy/media" {
		t.Errorf("mount points wrong: %+v", mp)
	}
	if !rec.Has("mount /dev/sda1 /run/autodeploy/esp") ||
		!rec.Has("mount /dev/sda2 /run/autodeploy/media") {
		t.Errorf("mount calls wrong\n%s", rec.Dump())
	}
	l.Unmount(context.Background(), rec, mp)
	if !rec.Has("umount /run/autodeploy/media") || !rec.Has("umount /run/autodeploy/esp") {
		t.Errorf("umount calls wrong\n%s", rec.Dump())
	}
}

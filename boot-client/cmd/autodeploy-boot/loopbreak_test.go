package main

import "testing"

func TestParseADBOOTPartNum(t *testing.T) {
	// A realistic `sgdisk -p` listing where Windows Setup has added its own
	// EFL/MSR/Windows partitions ahead of the trailing ADBOOT media partition.
	out := `Disk /dev/nvme0n1: 1000215216 sectors, 476.9 GiB
Number  Start (sector)    End (sector)  Size       Code  Name
   1            2048          788479   384.0 MiB   EF00  EFI system partition
   2          788480          821247   16.0 MiB    0C01  Microsoft reserved ...
   3          821248       985614335   469.9 GiB   0700  Basic data partition
   4       985614336       999999999   6.9 GiB     0700  ADBOOT
`
	if got := parseADBOOTPartNum(out); got != 4 {
		t.Errorf("parseADBOOTPartNum = %d, want 4", got)
	}
	// No ADBOOT partition (fresh disk) -> 0.
	none := `Number  Start (sector)    End (sector)  Size       Code  Name
   1            2048          788479   384.0 MiB   EF00  EFI system partition
`
	if got := parseADBOOTPartNum(none); got != 0 {
		t.Errorf("parseADBOOTPartNum(none) = %d, want 0", got)
	}
}

func TestPartitionDevice(t *testing.T) {
	cases := map[string]struct {
		disk string
		n    int
		want string
	}{
		"sata":  {"/dev/sda", 4, "/dev/sda4"},
		"nvme":  {"/dev/nvme0n1", 4, "/dev/nvme0n1p4"},
		"empty": {"", 1, ""},
	}
	for name, c := range cases {
		if got := partitionDevice(c.disk, c.n); got != c.want {
			t.Errorf("%s: partitionDevice(%q,%d) = %q, want %q", name, c.disk, c.n, got, c.want)
		}
	}
}

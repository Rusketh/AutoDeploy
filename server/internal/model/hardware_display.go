package model

import "fmt"

// Display helpers on Hardware so templates can render human-readable values
// without depending on a template funcmap entry. All are safe on a nil/zero
// receiver field (they operate on value receivers populated from JSON).

// MemoryDisplay formats total RAM as a rounded GiB string, e.g. "32 GB".
// Empty when unknown.
func (h Hardware) MemoryDisplay() string { return bytesDisplay(h.MemoryBytes) }

// SizeDisplay formats a disk's size, e.g. "931 GB". Empty when unknown.
func (d HardwareDisk) SizeDisplay() string { return bytesDisplay(d.SizeBytes) }

// bytesDisplay renders a byte count in the largest sensible binary unit.
// Returns "" for non-positive input so templates can omit the field.
func bytesDisplay(b int64) string {
	if b <= 0 {
		return ""
	}
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
		tib = gib * 1024
	)
	switch {
	case b >= tib:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(tib))
	case b >= gib:
		return fmt.Sprintf("%.0f GB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(mib))
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}

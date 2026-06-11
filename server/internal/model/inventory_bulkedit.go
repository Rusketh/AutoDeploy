package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// BindingEdit describes one bulk change applied to many machines'
// bindings. Nil fields are left untouched; pointing a field at its zero
// value clears it (image 0 unbinds, empty name/OU clears). Group edits
// are incremental: AddGroups joins, RemoveGroups leaves, both
// case-insensitive.
type BindingEdit struct {
	ImageID      *ID      `json:"image_id,omitempty"`
	MachineName  *string  `json:"machine_name,omitempty"`
	TargetOU     *string  `json:"target_ou,omitempty"`
	AddGroups    []string `json:"add_groups,omitempty"`
	RemoveGroups []string `json:"remove_groups,omitempty"`
}

// Empty reports whether the edit would change nothing.
func (e BindingEdit) Empty() bool {
	return e.ImageID == nil && e.MachineName == nil && e.TargetOU == nil &&
		len(e.AddGroups) == 0 && len(e.RemoveGroups) == 0
}

// BulkEditBindings applies one edit to each listed machine's binding
// (creating bindings for machines that have none). Unknown machine ids
// are skipped. Returns how many machines were updated.
//
// Bindings hold INTENDED state: a changed OU or name takes effect at the
// next deploy / AD join / rename, it does not move or rename machines by
// itself — same semantics as editing a single binding on the machine
// page.
func (r *InventoryRepo) BulkEditBindings(ctx context.Context, machineIDs []ID, e BindingEdit) (int, error) {
	if e.Empty() {
		return 0, fmt.Errorf("%w: nothing to change", ErrValidation)
	}
	if len(machineIDs) == 0 {
		return 0, fmt.Errorf("%w: at least one machine required", ErrValidation)
	}
	updated := 0
	for _, id := range machineIDs {
		if _, err := r.Get(ctx, id); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return updated, err
		}
		b, err := r.GetBinding(ctx, id)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return updated, err
			}
			b = MachineBinding{MachineID: id}
		}
		if e.ImageID != nil {
			if *e.ImageID == 0 {
				b.ImageID = nil
			} else {
				v := *e.ImageID
				b.ImageID = &v
			}
		}
		if e.MachineName != nil {
			b.MachineName = strings.TrimSpace(*e.MachineName)
		}
		if e.TargetOU != nil {
			b.TargetOU = strings.TrimSpace(*e.TargetOU)
		}
		b.GroupMemberships = applyGroupEdits(b.GroupMemberships, e.AddGroups, e.RemoveGroups)
		if err := r.UpsertBinding(ctx, b); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// applyGroupEdits removes then adds group names case-insensitively,
// preserving the existing order, deduping, and dropping blanks.
func applyGroupEdits(current, add, remove []string) []string {
	removeSet := map[string]bool{}
	for _, g := range remove {
		if k := strings.ToLower(strings.TrimSpace(g)); k != "" {
			removeSet[k] = true
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(current)+len(add))
	keep := func(g string) {
		g = strings.TrimSpace(g)
		k := strings.ToLower(g)
		if g == "" || removeSet[k] || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, g)
	}
	for _, g := range current {
		keep(g)
	}
	for _, g := range add {
		keep(g)
	}
	return out
}

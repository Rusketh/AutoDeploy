package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// MachineGroupRepo owns machine_group plus its membership tables
// (machine_group_member for direct machines, machine_group_subgroup for
// nesting). It depends on InventoryRepo to resolve dynamic and nested groups
// to a concrete machine set.
type MachineGroupRepo struct {
	db  *storage.DB
	inv *InventoryRepo
}

// NewMachineGroupRepo constructs the repo. inv is required: every membership
// read resolves through inventory.
func NewMachineGroupRepo(db *storage.DB, inv *InventoryRepo) *MachineGroupRepo {
	return &MachineGroupRepo{db: db, inv: inv}
}

// normaliseKind validates a group kind, defaulting blank to manual.
func normaliseKind(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "", MachineGroupManual:
		return MachineGroupManual, nil
	case MachineGroupDynamic:
		return MachineGroupDynamic, nil
	default:
		return "", fmt.Errorf("%w: unknown group kind %q", ErrValidation, kind)
	}
}

// validateGroup checks the name, normalises the kind, and validates the filter
// for dynamic groups. It clears the filter on a manual group so the two kinds
// stay clean. Mutates g in place.
func (r *MachineGroupRepo) validateGroup(g *MachineGroup) error {
	if err := validateName(g.Name); err != nil {
		return err
	}
	kind, err := normaliseKind(g.Kind)
	if err != nil {
		return err
	}
	g.Kind = kind
	if g.Kind == MachineGroupDynamic {
		if g.Filter.Empty() {
			return fmt.Errorf("%w: a dynamic group needs at least one filter criterion", ErrValidation)
		}
		if _, err := compileNameRegex(g.Filter.NameRegex); err != nil {
			return err
		}
	} else {
		g.Filter = MachineFilter{}
	}
	return nil
}

func (r *MachineGroupRepo) Create(ctx context.Context, g MachineGroup) (MachineGroup, error) {
	if err := r.validateGroup(&g); err != nil {
		return MachineGroup{}, err
	}
	if err := r.checkChildCycles(ctx, g.ID, g.Children); err != nil {
		return MachineGroup{}, err
	}
	filterJSON, _ := json.Marshal(g.Filter)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return MachineGroup{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO machine_group (name, description, kind, filter_json)
		VALUES (?, ?, ?, ?)`,
		g.Name, g.Description, g.Kind, string(filterJSON))
	if err != nil {
		if isUniqueErr(err) {
			return MachineGroup{}, fmt.Errorf("group %q: %w", g.Name, ErrConflict)
		}
		return MachineGroup{}, err
	}
	id64, _ := res.LastInsertId()
	id := ID(id64)
	if err := writeSubgroups(ctx, tx, id, g.Children); err != nil {
		return MachineGroup{}, err
	}
	if err := tx.Commit(); err != nil {
		return MachineGroup{}, err
	}
	return r.Get(ctx, id)
}

func (r *MachineGroupRepo) Update(ctx context.Context, g MachineGroup) error {
	if err := r.validateGroup(&g); err != nil {
		return err
	}
	if err := r.checkChildCycles(ctx, g.ID, g.Children); err != nil {
		return err
	}
	filterJSON, _ := json.Marshal(g.Filter)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE machine_group
		SET name=?, description=?, kind=?, filter_json=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		g.Name, g.Description, g.Kind, string(filterJSON), g.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("group %q: %w", g.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group %d: %w", g.ID, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM machine_group_subgroup WHERE group_id=?`, g.ID); err != nil {
		return err
	}
	if err := writeSubgroups(ctx, tx, g.ID, g.Children); err != nil {
		return err
	}
	// A group switched to dynamic sheds any direct machine members: dynamic
	// membership comes from the filter alone.
	if g.Kind == MachineGroupDynamic {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM machine_group_member WHERE group_id=?`, g.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// writeSubgroups inserts the child-group edges for groupID, skipping self-links
// and duplicates. Cycle safety is enforced by the caller via checkChildCycles.
func writeSubgroups(ctx context.Context, tx *sql.Tx, groupID ID, children []ID) error {
	seen := map[ID]bool{}
	for _, c := range children {
		if c <= 0 || c == groupID || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO machine_group_subgroup (group_id, child_group_id) VALUES (?, ?)`,
			groupID, c); err != nil {
			return err
		}
	}
	return nil
}

// checkChildCycles refuses a proposed child set that would create a nesting
// loop: a child equal to the group, or a child that can already reach the group
// through existing subgroup edges (so group -> child -> ... -> group).
func (r *MachineGroupRepo) checkChildCycles(ctx context.Context, groupID ID, children []ID) error {
	if len(children) == 0 {
		return nil
	}
	edges, err := r.allSubgroupEdges(ctx)
	if err != nil {
		return err
	}
	for _, c := range children {
		if c <= 0 {
			continue
		}
		if c == groupID {
			return fmt.Errorf("group %d: %w (a group cannot contain itself)", groupID, ErrCycle)
		}
		if groupID != 0 && reachableGroup(edges, c, groupID) {
			return fmt.Errorf("group %d: %w (nesting group %d would create a loop)", groupID, ErrCycle, c)
		}
	}
	return nil
}

// reachableGroup reports whether target is reachable from start by following
// subgroup edges. Iterative with a visited guard so a pre-existing bad cycle in
// the data cannot hang the walk.
func reachableGroup(edges map[ID][]ID, start, target ID) bool {
	if start == target {
		return true
	}
	visited := map[ID]bool{start: true}
	stack := append([]ID(nil), edges[start]...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == target {
			return true
		}
		if visited[n] {
			continue
		}
		visited[n] = true
		stack = append(stack, edges[n]...)
	}
	return false
}

func (r *MachineGroupRepo) allSubgroupEdges(ctx context.Context) (map[ID][]ID, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT group_id, child_group_id FROM machine_group_subgroup`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID][]ID{}
	for rows.Next() {
		var gid, cid ID
		if err := rows.Scan(&gid, &cid); err != nil {
			return nil, err
		}
		out[gid] = append(out[gid], cid)
	}
	return out, rows.Err()
}

// scanGroupInto scans one machine_group row through the supplied Scan method
// value (works for both QueryRow and Rows), decoding the filter JSON.
func scanGroupInto(g *MachineGroup, scan func(dest ...any) error) error {
	var filterJSON string
	if err := scan(&g.ID, &g.Name, &g.Description, &g.Kind, &filterJSON,
		&g.CreatedAt, &g.UpdatedAt); err != nil {
		return err
	}
	if strings.TrimSpace(filterJSON) != "" {
		_ = json.Unmarshal([]byte(filterJSON), &g.Filter)
	}
	return nil
}

func (r *MachineGroupRepo) Get(ctx context.Context, id ID) (MachineGroup, error) {
	var g MachineGroup
	err := scanGroupInto(&g, r.db.QueryRowContext(ctx, `
		SELECT id, name, description, kind, filter_json, created_at, updated_at
		FROM machine_group WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineGroup{}, fmt.Errorf("group %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return MachineGroup{}, err
	}
	children, err := r.childrenOf(ctx, id)
	if err != nil {
		return MachineGroup{}, err
	}
	g.Children = children
	return g, nil
}

func (r *MachineGroupRepo) childrenOf(ctx context.Context, id ID) ([]ID, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT child_group_id FROM machine_group_subgroup WHERE group_id=? ORDER BY child_group_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ID
	for rows.Next() {
		var cid ID
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

// List returns all groups ordered by name, with Children populated but member
// counts left at zero (use ListWithCounts for those).
func (r *MachineGroupRepo) List(ctx context.Context) ([]MachineGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, kind, filter_json, created_at, updated_at
		FROM machine_group ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineGroup
	for rows.Next() {
		var g MachineGroup
		if err := scanGroupInto(&g, rows.Scan); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	edges, err := r.allSubgroupEdges(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Children = edges[out[i].ID]
	}
	return out, nil
}

// ListWithCounts returns every group with MemberCount resolved. It loads the
// group graph and the inventory once and resolves all groups against that
// shared snapshot, so the cost is linear in (groups + machines) rather than
// re-scanning inventory per group.
func (r *MachineGroupRepo) ListWithCounts(ctx context.Context) ([]MachineGroup, error) {
	groups, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	graph, err := r.loadGraph(ctx, groups)
	if err != nil {
		return nil, err
	}
	inv, err := r.loadInvData(ctx)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		set := map[ID]bool{}
		collectGroupSet(groups[i].ID, graph, inv, map[ID]bool{}, set)
		groups[i].MemberCount = len(set)
	}
	return groups, nil
}

// groupGraph is the in-memory group structure used to resolve membership:
// kinds/filters by id, plus direct machine edges and child-group edges.
type groupGraph struct {
	byID     map[ID]MachineGroup
	members  map[ID][]ID
	children map[ID][]ID
}

func (r *MachineGroupRepo) loadGraph(ctx context.Context, groups []MachineGroup) (groupGraph, error) {
	g := groupGraph{byID: map[ID]MachineGroup{}, members: map[ID][]ID{}, children: map[ID][]ID{}}
	for _, grp := range groups {
		g.byID[grp.ID] = grp
	}
	mrows, err := r.db.QueryContext(ctx, `SELECT group_id, machine_id FROM machine_group_member`)
	if err != nil {
		return g, err
	}
	for mrows.Next() {
		var gid, mid ID
		if err := mrows.Scan(&gid, &mid); err != nil {
			mrows.Close()
			return g, err
		}
		g.members[gid] = append(g.members[gid], mid)
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return g, err
	}
	srows, err := r.db.QueryContext(ctx, `SELECT group_id, child_group_id FROM machine_group_subgroup`)
	if err != nil {
		return g, err
	}
	for srows.Next() {
		var gid, cid ID
		if err := srows.Scan(&gid, &cid); err != nil {
			srows.Close()
			return g, err
		}
		g.children[gid] = append(g.children[gid], cid)
	}
	srows.Close()
	return g, srows.Err()
}

// invData is the inventory snapshot dynamic resolution matches against.
type invData struct {
	recs  []MachineRecord
	binds map[ID]MachineBinding
	os    map[ID]string
}

func (r *MachineGroupRepo) loadInvData(ctx context.Context) (invData, error) {
	recs, err := r.inv.List(ctx)
	if err != nil {
		return invData{}, err
	}
	ids := make([]ID, len(recs))
	for i, m := range recs {
		ids[i] = m.ID
	}
	binds, err := r.inv.ListBindingsForMachines(ctx, ids)
	if err != nil {
		return invData{}, err
	}
	os, err := r.inv.ListOSCaptions(ctx)
	if err != nil {
		return invData{}, err
	}
	return invData{recs: recs, binds: binds, os: os}, nil
}

// collectGroupSet adds the resolved members of group id into set: dynamic
// filter matches, direct machine members, and recursively each child group's
// members. visited guards against nesting cycles.
func collectGroupSet(id ID, g groupGraph, inv invData, visited, set map[ID]bool) {
	if visited[id] {
		return
	}
	visited[id] = true
	grp, ok := g.byID[id]
	if !ok {
		return
	}
	if grp.Kind == MachineGroupDynamic && !grp.Filter.Empty() {
		if ids, err := matchMachineIDs(grp.Filter, inv.recs, inv.binds, inv.os); err == nil {
			for _, mid := range ids {
				set[mid] = true
			}
		}
	}
	for _, mid := range g.members[id] {
		set[mid] = true
	}
	for _, cid := range g.children[id] {
		collectGroupSet(cid, g, inv, visited, set)
	}
}

// resolve loads the graph + inventory and returns the resolved member ID set
// for one group. Shared by Members, MemberUUIDs, MemberRecords and
// GroupsForMachine.
func (r *MachineGroupRepo) resolve(ctx context.Context, id ID) (map[ID]bool, error) {
	groups, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	graph, err := r.loadGraph(ctx, groups)
	if err != nil {
		return nil, err
	}
	inv, err := r.loadInvData(ctx)
	if err != nil {
		return nil, err
	}
	set := map[ID]bool{}
	collectGroupSet(id, graph, inv, map[ID]bool{}, set)
	return set, nil
}

// Members returns the resolved machine IDs of a group, sorted. Works the same
// for manual, dynamic, and nested groups — the one read every consumer uses.
func (r *MachineGroupRepo) Members(ctx context.Context, id ID) ([]ID, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	set, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]ID, 0, len(set))
	for mid := range set {
		out = append(out, mid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// MemberCount is len(Members) without materialising the slice for callers that
// only need the number.
func (r *MachineGroupRepo) MemberCount(ctx context.Context, id ID) (int, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return 0, err
	}
	set, err := r.resolve(ctx, id)
	if err != nil {
		return 0, err
	}
	return len(set), nil
}

// MemberUUIDs returns the SMBIOS UUIDs of a group's members, for consumers
// keyed on UUID (the activity log's actor). Machines with an empty UUID are
// omitted.
func (r *MachineGroupRepo) MemberUUIDs(ctx context.Context, id ID) ([]string, error) {
	set, err := r.membersChecked(ctx, id)
	if err != nil {
		return nil, err
	}
	recs, err := r.inv.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range recs {
		if set[m.ID] && m.SystemUUID != "" {
			out = append(out, m.SystemUUID)
		}
	}
	return out, nil
}

// MemberRecords returns the full machine records for a group's members, ordered
// as inventory returns them (most recently seen first).
func (r *MachineGroupRepo) MemberRecords(ctx context.Context, id ID) ([]MachineRecord, error) {
	set, err := r.membersChecked(ctx, id)
	if err != nil {
		return nil, err
	}
	recs, err := r.inv.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []MachineRecord
	for _, m := range recs {
		if set[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// membersChecked resolves a group after confirming it exists.
func (r *MachineGroupRepo) membersChecked(ctx context.Context, id ID) (map[ID]bool, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	return r.resolve(ctx, id)
}

// GroupsForMachine returns every group the machine resolves into, for the
// machine-detail page. Resolves all groups against one shared snapshot.
func (r *MachineGroupRepo) GroupsForMachine(ctx context.Context, machineID ID) ([]MachineGroup, error) {
	groups, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	graph, err := r.loadGraph(ctx, groups)
	if err != nil {
		return nil, err
	}
	inv, err := r.loadInvData(ctx)
	if err != nil {
		return nil, err
	}
	var out []MachineGroup
	for _, g := range groups {
		set := map[ID]bool{}
		collectGroupSet(g.ID, graph, inv, map[ID]bool{}, set)
		if set[machineID] {
			out = append(out, g)
		}
	}
	return out, nil
}

// AddMembers adds machines to a manual group (idempotent). Refuses a dynamic
// group, whose membership is computed.
func (r *MachineGroupRepo) AddMembers(ctx context.Context, id ID, machineIDs []ID) error {
	return r.mutateMembers(ctx, id, machineIDs,
		`INSERT OR IGNORE INTO machine_group_member (group_id, machine_id) VALUES (?, ?)`)
}

// RemoveMembers removes machines from a manual group.
func (r *MachineGroupRepo) RemoveMembers(ctx context.Context, id ID, machineIDs []ID) error {
	return r.mutateMembers(ctx, id, machineIDs,
		`DELETE FROM machine_group_member WHERE group_id=? AND machine_id=?`)
}

func (r *MachineGroupRepo) mutateMembers(ctx context.Context, id ID, machineIDs []ID, stmt string) error {
	g, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if g.Kind != MachineGroupManual {
		return fmt.Errorf("%w: %q is a dynamic group; its membership comes from its filter", ErrValidation, g.Name)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, mid := range machineIDs {
		if mid <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt, id, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes a group. Member and subgroup edges cascade, including edges
// where this group was nested as a child of another.
func (r *MachineGroupRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM machine_group WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group %d: %w", id, ErrNotFound)
	}
	return nil
}

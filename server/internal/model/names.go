package model

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// MachineName is the label a machine is shown under everywhere (portal
// pages, notifications, log rows). Build it with MachineDisplayName so every
// surface agrees on the precedence.
type MachineName struct {
	// Name is the label to show: the desired (binding) name when set, else
	// the agent-reported computer name, else the short SMBIOS UUID.
	Name string `json:"name"`
	// RenamingFrom is the agent-reported name when the desired name differs
	// from it, i.e. a rename is pending (it applies on the next rename job or
	// re-image). Empty when the two agree or either is unknown.
	RenamingFrom string `json:"renaming_from,omitempty"`
	// Unnamed is true when neither a desired nor a reported name is known and
	// Name is only the short-UUID fallback.
	Unnamed bool `json:"unnamed,omitempty"`
}

// String returns the display label.
func (n MachineName) String() string { return n.Name }

// MachineDisplayName returns the single display-name precedence used for a
// machine across the server:
//
//  1. the binding's desired MachineName (skipped when it is a %placeholder%
//     template, which is not a real computer name);
//  2. the agent-reported Windows computer name;
//  3. the first 8 characters of the SMBIOS UUID (or "machine #ID").
//
// When the desired name wins and the agent reports a different current name,
// RenamingFrom carries the reported one so callers can show a
// "renaming from X" hint.
func MachineDisplayName(m MachineRecord, b MachineBinding) MachineName {
	desired := strings.TrimSpace(b.MachineName)
	if strings.Contains(desired, "%") {
		desired = ""
	}
	reported := strings.TrimSpace(m.ReportedName)
	switch {
	case desired != "":
		n := MachineName{Name: desired}
		if reported != "" && !strings.EqualFold(reported, desired) {
			n.RenamingFrom = reported
		}
		return n
	case reported != "":
		return MachineName{Name: reported}
	}
	return MachineName{Name: ShortMachineID(m), Unnamed: true}
}

// ShortMachineID is the fallback label for a machine with no known name: the
// first 8 characters of its SMBIOS UUID followed by an ellipsis, or
// "machine #ID" when the UUID is empty.
func ShortMachineID(m MachineRecord) string {
	u := strings.TrimSpace(m.SystemUUID)
	if u == "" {
		return fmt.Sprintf("machine #%d", int64(m.ID))
	}
	if len(u) > 8 {
		return u[:8] + "…"
	}
	return u
}

// MachineNameIndex is every machine's display name, plus lookups from the
// identifiers log rows and API callers carry instead of the row ID.
type MachineNameIndex struct {
	ByID map[ID]MachineName
	// ByActor maps both the SMBIOS UUID and the server-minted agent ID to the
	// machine ID. Older log rows (and update.jobs.claimed before this release)
	// hold the agent ID as their actor; agent/boot-client rows hold the UUID.
	ByActor map[string]ID
}

// NameIndex loads every machine's display name in one query (machine_record
// LEFT JOIN machine_binding) — never one query per machine.
func (r *InventoryRepo) NameIndex(ctx context.Context) (MachineNameIndex, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.system_uuid, m.agent_id, m.reported_name, COALESCE(b.machine_name, '')
		FROM machine_record m LEFT JOIN machine_binding b ON b.machine_id = m.id`)
	if err != nil {
		return MachineNameIndex{}, err
	}
	defer rows.Close()
	idx := MachineNameIndex{ByID: map[ID]MachineName{}, ByActor: map[string]ID{}}
	for rows.Next() {
		var m MachineRecord
		var b MachineBinding
		if err := rows.Scan(&m.ID, &m.SystemUUID, &m.AgentID, &m.ReportedName, &b.MachineName); err != nil {
			return MachineNameIndex{}, err
		}
		idx.ByID[m.ID] = MachineDisplayName(m, b)
		if m.SystemUUID != "" {
			idx.ByActor[m.SystemUUID] = m.ID
		}
		if m.AgentID != "" {
			idx.ByActor[m.AgentID] = m.ID
		}
	}
	return idx, rows.Err()
}

// Entity kinds understood by NameLookup and ParseLogTarget.
const (
	KindMachine    = "machine"
	KindImage      = "image"
	KindISO        = "iso"
	KindUnattend   = "unattend"
	KindDriver     = "driver"
	KindSoftware   = "software"
	KindLoadout    = "loadout"
	KindImageGroup = "image_group"
	KindGroup      = "group" // AutoDeploy machine group
	KindBulk       = "bulk"
	KindMirror     = "mirror"
	KindUpdate     = "update" // Windows Update KB
	KindWebhook    = "webhook"
)

const kindQueryPrefix = "SELECT id, "

// kindQueries is the id → label query for each simple kind. Bulk operations
// are handled separately (their name can be blank on old rows).
var kindQueries = map[string]string{
	KindImage:      kindQueryPrefix + "name FROM image",
	KindISO:        kindQueryPrefix + "name FROM iso",
	KindUnattend:   kindQueryPrefix + "name FROM unattend",
	KindDriver:     kindQueryPrefix + "name FROM driver_package",
	KindSoftware:   kindQueryPrefix + "name FROM software_package",
	KindLoadout:    kindQueryPrefix + "name FROM software_loadout",
	KindImageGroup: kindQueryPrefix + "name FROM image_group",
	KindGroup:      kindQueryPrefix + "name FROM machine_group",
	KindMirror:     kindQueryPrefix + "name FROM payload_mirror",
	KindUpdate:     kindQueryPrefix + "kb_number FROM windows_update",
	KindWebhook:    kindQueryPrefix + "name FROM webhook",
}

// kindAliases normalises the spellings that appear in log targets and
// callers to a Kind* constant.
var kindAliases = map[string]string{
	"machine": KindMachine, "machines": KindMachine,
	"image": KindImage, "images": KindImage,
	"iso": KindISO, "isos": KindISO,
	"unattend": KindUnattend, "unattends": KindUnattend,
	"driver": KindDriver, "drivers": KindDriver, "driver_package": KindDriver,
	"software": KindSoftware, "software_package": KindSoftware, "package": KindSoftware,
	"loadout": KindLoadout, "loadouts": KindLoadout, "software_loadout": KindLoadout,
	"image_group": KindImageGroup,
	"group":       KindGroup, "groups": KindGroup, "machine_group": KindGroup,
	"bulk": KindBulk, "bulk_operation": KindBulk, "operation": KindBulk,
	"mirror": KindMirror, "mirrors": KindMirror, "payload_mirror": KindMirror,
	"update": KindUpdate, "updates": KindUpdate, "windows_update": KindUpdate,
	"webhook": KindWebhook,
}

// NormalizeKind maps a kind spelling ("software_package", "machines") to its
// Kind* constant, or "" when unknown.
func NormalizeKind(kind string) string {
	return kindAliases[strings.ToLower(strings.TrimSpace(kind))]
}

// ParseLogTarget splits a log event target such as "machine:12",
// "software/12" or "machine:F45ABB24-…" into a kind and reference. ref is the
// raw text after the separator; id is set when it is a positive integer.
// Targets that don't name a known kind ("machines:5" counts, "group:Lab"
// names, free text) return ok=false, except a non-numeric machine reference,
// which NameLookup resolves as a UUID / agent ID.
func ParseLogTarget(target string) (kind string, id ID, ref string, ok bool) {
	t := strings.TrimSpace(target)
	i := strings.IndexAny(t, ":/")
	if i <= 0 || i == len(t)-1 {
		return "", 0, "", false
	}
	raw := t[:i]
	if strings.EqualFold(raw, "machines") {
		return "", 0, "", false // "machines:5" is a count, not an ID
	}
	kind = NormalizeKind(raw)
	if kind == "" {
		return "", 0, "", false
	}
	ref = strings.TrimSpace(t[i+1:])
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil && n > 0 {
		return kind, ID(n), ref, true
	}
	if kind == KindMachine && ref != "" {
		return kind, 0, ref, true
	}
	return "", 0, "", false
}

// NameResult is one NameLookup answer.
type NameResult struct {
	Name string
	// Found is true when the row exists. When Found is false and Loaded is
	// true the row was deleted; when Loaded is false the kind could not be
	// read (unknown kind or a database error), so the caller should not claim
	// the row is gone.
	Found  bool
	Loaded bool
}

// Label is the text to show: the name, "#12 (deleted)" for a row that no
// longer exists, or "#12" when the kind could not be loaded.
func (n NameResult) Label(id ID) string {
	switch {
	case n.Found:
		return n.Name
	case n.Loaded:
		return fmt.Sprintf("#%d (deleted)", int64(id))
	}
	return fmt.Sprintf("#%d", int64(id))
}

// NameLookup resolves entity IDs to display names for one request. Each
// kind's id → name map is loaded at most once, on first use, so a page that
// labels 500 rows costs one query per kind instead of one per row. Safe for
// concurrent use. A nil *NameLookup (or one built without a repo) resolves
// nothing but never fails, which keeps template tests simple.
type NameLookup struct {
	ctx  context.Context
	db   *sql.DB
	inv  *InventoryRepo
	mu   sync.Mutex
	maps map[string]map[ID]string
	errs map[string]bool
	mach *MachineNameIndex
}

// NewNameLookup returns a lazy lookup reading from the inventory repo's
// database. Every entity table lives in the same SQLite database, so the one
// handle serves every kind. inv may be nil.
func NewNameLookup(ctx context.Context, inv *InventoryRepo) *NameLookup {
	l := &NameLookup{ctx: ctx, inv: inv, maps: map[string]map[ID]string{}, errs: map[string]bool{}}
	if inv != nil && inv.db != nil {
		l.db = inv.db.DB
	}
	return l
}

func (l *NameLookup) kindMap(kind string) (map[ID]string, bool) {
	if l == nil || l.db == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if m, ok := l.maps[kind]; ok {
		return m, true
	}
	if l.errs[kind] {
		return nil, false
	}
	m, err := l.load(kind)
	if err != nil {
		l.errs[kind] = true
		return nil, false
	}
	l.maps[kind] = m
	return m, true
}

func (l *NameLookup) load(kind string) (map[ID]string, error) {
	if kind == KindBulk {
		return l.loadBulk()
	}
	q, ok := kindQueries[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	rows, err := l.db.QueryContext(l.ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]string{}
	for rows.Next() {
		var id ID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// loadBulk labels bulk operations by name, falling back to "<Action> #ID"
// for rows written before operations had names.
func (l *NameLookup) loadBulk() (map[ID]string, error) {
	rows, err := l.db.QueryContext(l.ctx, `SELECT id, name, action FROM bulk_operation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]string{}
	for rows.Next() {
		var id ID
		var name, action string
		if err := rows.Scan(&id, &name, &action); err != nil {
			return nil, err
		}
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("%s #%d", BulkActionLabel(action), int64(id))
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (l *NameLookup) machines() (*MachineNameIndex, bool) {
	if l == nil || l.inv == nil || l.db == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mach != nil {
		return l.mach, true
	}
	if l.errs[KindMachine] {
		return nil, false
	}
	idx, err := l.inv.NameIndex(l.ctx)
	if err != nil {
		l.errs[KindMachine] = true
		return nil, false
	}
	l.mach = &idx
	return l.mach, true
}

// Lookup resolves one entity. kind may be any spelling NormalizeKind knows.
func (l *NameLookup) Lookup(kind string, id ID) NameResult {
	k := NormalizeKind(kind)
	if k == KindMachine {
		idx, ok := l.machines()
		if !ok {
			return NameResult{}
		}
		n, found := idx.ByID[id]
		return NameResult{Name: n.Name, Found: found, Loaded: true}
	}
	m, ok := l.kindMap(k)
	if !ok {
		return NameResult{}
	}
	name, found := m[id]
	return NameResult{Name: name, Found: found, Loaded: true}
}

// Name is Lookup reduced to the label text and whether the row exists.
func (l *NameLookup) Name(kind string, id ID) (string, bool) {
	res := l.Lookup(kind, id)
	return res.Name, res.Found
}

// Machine returns a machine's display name (with the pending-rename hint).
func (l *NameLookup) Machine(id ID) (MachineName, bool) {
	idx, ok := l.machines()
	if !ok {
		return MachineName{}, false
	}
	n, found := idx.ByID[id]
	return n, found
}

// MachineByActor resolves an SMBIOS UUID or agent ID (as found in a log
// row's actor) to the machine's ID and display name.
func (l *NameLookup) MachineByActor(actor string) (ID, MachineName, bool) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return 0, MachineName{}, false
	}
	idx, ok := l.machines()
	if !ok {
		return 0, MachineName{}, false
	}
	id, found := idx.ByActor[actor]
	if !found {
		// SMBIOS UUIDs are case-insensitive; agents have shipped both cases.
		id, found = idx.ByActor[strings.ToUpper(actor)]
	}
	if !found {
		return 0, MachineName{}, false
	}
	return id, idx.ByID[id], true
}

// EnrichLogs fills the read-time name fields on each event: MachineName from
// a machine actor (SMBIOS UUID, or the agent ID older rows carry) or, failing
// that, from a machine target; TargetName/TargetKind/TargetID from a target
// such as "machine:12" or "software/12". Unresolvable values are left blank
// so callers fall back to the raw strings.
func (l *NameLookup) EnrichLogs(events []LogEvent) {
	if l == nil {
		return
	}
	for i := range events {
		ev := &events[i]
		if _, n, ok := l.MachineByActor(ev.Actor); ok {
			ev.MachineName = n.Name
		}
		kind, id, ref, ok := ParseLogTarget(ev.Target)
		if !ok {
			continue
		}
		if kind == KindMachine && id == 0 {
			mid, n, found := l.MachineByActor(ref)
			if !found {
				continue
			}
			id = mid
			ev.TargetName = n.Name
		} else {
			res := l.Lookup(kind, id)
			if !res.Loaded {
				continue
			}
			ev.TargetName = res.Label(id)
		}
		ev.TargetKind, ev.TargetID = kind, id
		if kind == KindMachine && ev.MachineName == "" {
			if res := l.Lookup(KindMachine, id); res.Found {
				ev.MachineName = res.Name
			}
		}
	}
}

// InUseError is returned by a repository Delete when other rows still
// reference the object. errors.Is(err, ErrInUse) matches it, and Error()
// keeps the historical "iso 7: in use (referenced by 3 images)" wording so
// API clients see the same text; Friendly() is the operator-facing sentence.
type InUseError struct {
	Kind string // "iso", "image", "loadout", "unattend"
	ID   ID
	Name string // the object's name ("" if unknown)
	// Refs groups the referencing rows by kind, in display order.
	Refs []InUseRef
	// legacy is the old parenthesised detail, kept verbatim for Error().
	legacy string
}

// InUseRef is one group of referencing rows: Noun is the singular noun
// ("image", "child loadout") and Names the referencing rows' names.
type InUseRef struct {
	Noun  string
	Names []string
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("%s %d: %v (%s)", e.Kind, int64(e.ID), ErrInUse, e.legacy)
}

// Is makes errors.Is(err, ErrInUse) true.
func (e *InUseError) Is(target error) bool { return target == ErrInUse }

// Count is the total number of referencing rows.
func (e *InUseError) Count() int {
	n := 0
	for _, r := range e.Refs {
		n += len(r.Names)
	}
	return n
}

// maxInUseNames caps how many referencing names Friendly lists per group.
const maxInUseNames = 5

// Friendly renders the operator-facing message, e.g.
// `Can't delete "Win11 23H2": used by 3 images (Lab, Office, Kiosk)`.
func (e *InUseError) Friendly() string {
	subject := e.Name
	if subject == "" {
		subject = fmt.Sprintf("%s #%d", e.Kind, int64(e.ID))
	}
	var parts []string
	for _, r := range e.Refs {
		if len(r.Names) == 0 {
			continue
		}
		noun := r.Noun
		if len(r.Names) != 1 {
			noun += "s"
		}
		shown := r.Names
		more := ""
		if len(shown) > maxInUseNames {
			more = fmt.Sprintf(", +%d more", len(shown)-maxInUseNames)
			shown = shown[:maxInUseNames]
		}
		parts = append(parts, fmt.Sprintf("%d %s (%s%s)", len(r.Names), noun, strings.Join(shown, ", "), more))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Can't delete %q: it is still in use", subject)
	}
	return fmt.Sprintf("Can't delete %q: used by %s", subject, strings.Join(parts, " and "))
}

// queryNames runs a SELECT name ... query inside tx and returns the names.
func queryNames(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// rowName reads one row's name inside tx ("" when missing).
func rowName(ctx context.Context, tx *sql.Tx, table string, id ID) string {
	var n string
	_ = tx.QueryRowContext(ctx, `SELECT name FROM `+table+` WHERE id=?`, id).Scan(&n)
	return n
}

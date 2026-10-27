package portal

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// This file holds the per-request name resolver and the template helpers
// that turn entity IDs into names and links, plus the shared time helpers.
// Templates should never print a bare "#{{derefID .X}}" for another row:
// use {{entityName "image" .ImageID}} / {{entityLink "image" .ImageID}}
// instead (TestTemplatesDontPrintRawIDs enforces it). A row's OWN number
// (job #12, deployment #3) goes through {{idLabel .ID}}.

type ctxKeyNames struct{}

// withNames attaches a lazy NameLookup to the request context so the handler
// and the templates it renders share one set of loaded name maps. Nothing is
// queried until something asks for a name.
func withNames(ctx context.Context, r Repos) context.Context {
	return context.WithValue(ctx, ctxKeyNames{}, model.NewNameLookup(ctx, r.Inventory))
}

// namesFor returns the request's NameLookup (attached by requireSession), or
// a fresh one for requests that bypassed the middleware (tests, login page).
func namesFor(req *http.Request, r Repos) *model.NameLookup {
	if l, ok := req.Context().Value(ctxKeyNames{}).(*model.NameLookup); ok && l != nil {
		return l
	}
	return model.NewNameLookup(req.Context(), r.Inventory)
}

// entityPaths is the portal page each entity kind links to.
var entityPaths = map[string]string{
	model.KindMachine:    "/portal/machines/%d",
	model.KindImage:      "/portal/images/%d/edit",
	model.KindISO:        "/portal/isos/%d/edit",
	model.KindUnattend:   "/portal/unattends/%d/edit",
	model.KindDriver:     "/portal/drivers/%d/edit",
	model.KindSoftware:   "/portal/software/%d/edit",
	model.KindLoadout:    "/portal/loadouts/%d/edit",
	model.KindGroup:      "/portal/groups/%d/edit",
	model.KindImageGroup: "/portal/images/groups",
	model.KindBulk:       "/portal/bulk/%d",
	model.KindMirror:     "/portal/mirrors/%d/edit",
	model.KindUpdate:     "/portal/updates/%d/edit",
	model.KindWebhook:    "/portal/settings/notifications/webhooks/%d",
}

// entityURL is the portal URL for one entity, or "" for an unknown kind.
func entityURL(kind string, id model.ID) string {
	p, ok := entityPaths[model.NormalizeKind(kind)]
	if !ok {
		return ""
	}
	if !strings.Contains(p, "%d") {
		return p
	}
	return fmt.Sprintf(p, int64(id))
}

// toID accepts the ID shapes templates hold: model.ID, *model.ID, int64,
// int and decimal strings. ok is false for nil pointers, zero and junk.
func toID(v any) (model.ID, bool) {
	var id model.ID
	switch x := v.(type) {
	case model.ID:
		id = x
	case *model.ID:
		if x == nil {
			return 0, false
		}
		id = *x
	case int64:
		id = model.ID(x)
	case int:
		id = model.ID(x)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, false
		}
		id = model.ID(n)
	default:
		return 0, false
	}
	return id, id > 0
}

// idLabel renders a row's own number ("#12"), for job / deployment numbers
// where the ID really is the identifier the operator refers to.
func idLabel(v any) string {
	id, ok := toID(v)
	if !ok {
		return ""
	}
	return "#" + strconv.FormatInt(int64(id), 10)
}

// nameFuncs returns the template helpers backed by the request's lookup.
//
//	entityName "image" .ImageID   -> "Win11 Lab", "#12 (deleted)", or "" for nil
//	entityLink "image" .ImageID   -> <a href=…>Win11 Lab</a>, muted "#12 (deleted)",
//	                                 or a muted dash for nil
//	machineName .MachineID        -> model.MachineName (.Name, .RenamingFrom, .Unnamed)
//	renameHint  .MachineID        -> muted "renaming from X" hint, or ""
//	logTarget   .                 -> a LogEvent's target as a name link, else the raw text
func nameFuncs(names *model.NameLookup) template.FuncMap {
	link := func(kind string, v any) template.HTML { return entityLinkHTML(names, kind, v) }
	return template.FuncMap{
		"entityName": func(kind string, v any) string { return entityLabel(names, kind, v) },
		"entityLink": link,
		"entityURL": func(kind string, v any) string {
			id, ok := toID(v)
			if !ok {
				return ""
			}
			return entityURL(kind, id)
		},
		"machineName": func(v any) model.MachineName {
			id, ok := toID(v)
			if !ok {
				return model.MachineName{}
			}
			if n, found := names.Machine(id); found {
				return n
			}
			return model.MachineName{Name: names.Lookup(model.KindMachine, id).Label(id), Unnamed: true}
		},
		"renameHint": func(v any) template.HTML {
			id, ok := toID(v)
			if !ok {
				return ""
			}
			n, found := names.Machine(id)
			if !found || n.RenamingFrom == "" {
				return ""
			}
			return renameHintHTML(n)
		},
		"idLabel": idLabel,
		"logTarget": func(ev model.LogEvent) template.HTML {
			if ev.TargetKind != "" && ev.TargetName != "" {
				return link(ev.TargetKind, ev.TargetID)
			}
			if ev.Target == "" {
				return template.HTML(`<span class="muted">—</span>`)
			}
			return template.HTML(template.HTMLEscapeString(ev.Target))
		},
	}
}

// entityLabel is the text for one entity reference: its name, "#12
// (deleted)", "#12" when the kind could not be loaded, or "" for nil/zero.
func entityLabel(names *model.NameLookup, kind string, v any) string {
	id, ok := toID(v)
	if !ok {
		return ""
	}
	return names.Lookup(kind, id).Label(id)
}

// entityLinkHTML links one entity reference by name. A deleted row renders
// as muted "#12 (deleted)" text (no dead link), nil/zero as a muted dash,
// and a machine with a pending rename carries the old name in its tooltip.
// Handlers that build HTML fragments for a template use it directly.
func entityLinkHTML(names *model.NameLookup, kind string, v any) template.HTML {
	id, ok := toID(v)
	if !ok {
		return template.HTML(`<span class="muted">—</span>`)
	}
	res := names.Lookup(kind, id)
	esc := template.HTMLEscapeString(res.Label(id))
	if res.Loaded && !res.Found {
		return template.HTML(`<span class="muted entity-deleted" title="This ` +
			template.HTMLEscapeString(kindNoun(kind)) + ` no longer exists">` + esc + `</span>`)
	}
	href := entityURL(kind, id)
	if href == "" {
		return template.HTML(esc)
	}
	title := ""
	if model.NormalizeKind(kind) == model.KindMachine {
		if n, found := names.Machine(id); found && n.RenamingFrom != "" {
			title = ` title="Currently reported as ` + template.HTMLEscapeString(n.RenamingFrom) +
				`; the new name applies on the next rename or re-image"`
		}
	}
	return template.HTML(`<a href="` + template.HTMLEscapeString(href) + `"` + title + `>` + esc + `</a>`)
}

// renameHintHTML is the muted "renaming from X" hint shown next to a machine
// whose desired name differs from what its agent reports.
func renameHintHTML(n model.MachineName) template.HTML {
	return template.HTML(`<span class="rename-hint small muted" title="The agent still reports ` +
		template.HTMLEscapeString(n.RenamingFrom) +
		`; the desired name applies on the next rename or re-image">renaming from ` +
		template.HTMLEscapeString(n.RenamingFrom) + `</span>`)
}

// kindNoun is the plain-English noun for an entity kind, used in error
// pages and tooltips ("That image no longer exists").
func kindNoun(kind string) string {
	switch model.NormalizeKind(kind) {
	case model.KindMachine:
		return "machine"
	case model.KindImage:
		return "image"
	case model.KindISO:
		return "ISO"
	case model.KindUnattend:
		return "unattend file"
	case model.KindDriver:
		return "driver package"
	case model.KindSoftware:
		return "software package"
	case model.KindLoadout:
		return "loadout"
	case model.KindGroup:
		return "machine group"
	case model.KindImageGroup:
		return "image group"
	case model.KindBulk:
		return "bulk operation"
	case model.KindMirror:
		return "mirror"
	case model.KindUpdate:
		return "update"
	case model.KindWebhook:
		return "webhook"
	}
	return "item"
}

// ---- Time helpers -----------------------------------------------------

// toTime accepts time.Time and *time.Time (nil -> zero).
func toTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case *time.Time:
		if x != nil {
			return *x
		}
	}
	return time.Time{}
}

// absTime formats t in loc with the zone abbreviation, e.g.
// "27 Oct 2026, 08:53:09 GMT".
func absTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2 Jan 2006, 15:04:05 MST")
}

// relativeTime is the short relative form used in lists: "just now",
// "5m ago", "3h ago", "4d ago", "in 2h" for future times, and the date for
// anything older than 30 days.
func relativeTime(t, now time.Time, loc *time.Location) string {
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < 90*time.Second:
		return "just now"
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		s = fmt.Sprintf("%dd", int(d.Hours())/24)
	default:
		return t.In(loc).Format("2 Jan 2006")
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

// timeFuncs returns the shared time helpers for the display zone loc:
//
//	timeAgo  .LastSeen  -> <time datetime=RFC3339 title="27 Oct 2026, 08:53:09 GMT">5m ago</time>
//	timeAbs  .LastSeen  -> <time datetime=RFC3339>27 Oct 2026, 08:53:09 GMT</time>
//	timeUnix .LastSeen  -> Unix seconds (0 for zero/nil), for data-sort-value
//
// All accept time.Time or *time.Time and render a muted dash for zero/nil.
func timeFuncs(loc *time.Location, now func() time.Time) template.FuncMap {
	tag := func(v any, rel bool) template.HTML {
		t := toTime(v)
		if t.IsZero() {
			return template.HTML(`<span class="muted">—</span>`)
		}
		abs := template.HTMLEscapeString(absTime(t, loc))
		text := abs
		if rel {
			text = template.HTMLEscapeString(relativeTime(t, now(), loc))
		}
		return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) +
			`" title="` + abs + `">` + text + `</time>`)
	}
	return template.FuncMap{
		"timeAgo": func(v any) template.HTML { return tag(v, true) },
		"timeAbs": func(v any) template.HTML { return tag(v, false) },
		"timeUnix": func(v any) int64 {
			t := toTime(v)
			if t.IsZero() {
				return 0
			}
			return t.Unix()
		},
	}
}

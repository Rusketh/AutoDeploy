package portal

import "strings"

// navItem is one primary-navigation link. Prefixes are the extra URL
// prefixes (besides URL itself) whose pages belong to this item, e.g.
// /portal/update-deployments under Windows Updates.
type navItem struct {
	Label    string
	URL      string
	Icon     string
	Prefixes []string
	On       bool
}

// navGroup is one dropdown (Fleet / Build / Patching / System).
type navGroup struct {
	Key   string
	Label string
	Icon  string
	Items []navItem
	On    bool
}

// navLayout is the grouped primary navigation. URLs are the existing portal
// routes, unchanged; only the grouping and labels are presentation.
var navLayout = []navGroup{
	{Key: "fleet", Label: "Fleet", Icon: "i-fleet", Items: []navItem{
		{Label: "Machines", URL: "/portal/machines", Icon: "i-server"},
		{Label: "Groups", URL: "/portal/groups", Icon: "i-users"},
		{Label: "Bulk", URL: "/portal/bulk", Icon: "i-bolt"},
		{Label: "Import", URL: "/portal/import", Icon: "i-upload"},
	}},
	{Key: "build", Label: "Build", Icon: "i-wrench", Items: []navItem{
		{Label: "Images", URL: "/portal/images", Icon: "i-image"},
		{Label: "ISOs", URL: "/portal/isos", Icon: "i-disc"},
		{Label: "Unattends", URL: "/portal/unattends", Icon: "i-file"},
		{Label: "Drivers", URL: "/portal/drivers", Icon: "i-cpu"},
		{Label: "Software", URL: "/portal/software", Icon: "i-box"},
		{Label: "Loadouts", URL: "/portal/loadouts", Icon: "i-stack"},
	}},
	{Key: "patching", Label: "Patching", Icon: "i-shield-check", Items: []navItem{
		{Label: "Windows Updates", URL: "/portal/updates", Icon: "i-refresh", Prefixes: []string{"/portal/update-deployments"}},
		{Label: "Mirrors", URL: "/portal/mirrors", Icon: "i-globe"},
	}},
	{Key: "system", Label: "System", Icon: "i-sliders", Items: []navItem{
		{Label: "Logs", URL: "/portal/logs", Icon: "i-list"},
		{Label: "Settings", URL: "/portal/settings", Icon: "i-gear", Prefixes: []string{"/portal/downloads"}},
	}},
}

// pathUnder reports whether path is prefix itself or a page beneath it
// (/portal/machines/12 is under /portal/machines; /portal/machinesX is not).
func pathUnder(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+".")
}

// buildNav returns a copy of navLayout with On set for the item (and its
// group) whose URL prefix matches path.
func buildNav(path string) []navGroup {
	out := make([]navGroup, len(navLayout))
	for gi, g := range navLayout {
		g.Items = append([]navItem(nil), g.Items...)
		g.On = false
		for ii := range g.Items {
			it := &g.Items[ii]
			it.On = pathUnder(path, it.URL)
			for _, p := range it.Prefixes {
				if pathUnder(path, p) {
					it.On = true
				}
			}
			if it.On {
				g.On = true
			}
		}
		out[gi] = g
	}
	return out
}

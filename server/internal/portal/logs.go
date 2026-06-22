package portal

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerLogsRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/logs", logsView(r))
	}
}

func logsView(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		s := model.LogSearch{
			Component: strings.TrimSpace(q.Get("component")),
			Actor:     strings.TrimSpace(q.Get("actor")),
			Action:    strings.TrimSpace(q.Get("action")),
			Limit:     200,
		}
		if v := strings.TrimSpace(q.Get("since")); v != "" {
			if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
				s.Since = t
			}
		}
		if v := strings.TrimSpace(q.Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				s.Limit = n
			}
		}
		// Group filter: restrict to events whose actor is one of the group's
		// member machines (resolved to their SMBIOS UUIDs). An empty group
		// forces an empty result rather than silently showing the whole log.
		var groupID int64
		if gs := strings.TrimSpace(q.Get("group")); gs != "" {
			if gid, perr := strconv.ParseInt(gs, 10, 64); perr == nil && gid > 0 {
				groupID = gid
				if uuids, uerr := r.Groups.MemberUUIDs(req.Context(), model.ID(gid)); uerr == nil {
					if len(uuids) == 0 {
						s.Actors = []string{"\x00no-member"}
					} else {
						s.Actors = uuids
					}
				}
			}
		}
		ev, err := r.Logs.Search(req.Context(), s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Resolve each agent event's actor (an SMBIOS UUID) to a machine
		// name so rows show a name instead of a raw UUID.
		if names, nerr := r.Inventory.NamesByUUID(req.Context()); nerr == nil {
			model.EnrichMachineNames(ev, names)
		}
		groups, _ := r.Groups.List(req.Context())
		render(w, req, r, "logs.html", "Logs", map[string]any{
			"Events":  ev,
			"Query":   s,
			"Groups":  groups,
			"GroupID": model.ID(groupID),
		})
	}
}

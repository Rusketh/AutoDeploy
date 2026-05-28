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
		ev, err := r.Logs.Search(req.Context(), s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "logs.html", "Logs", map[string]any{
			"Events": ev,
			"Query":  s,
		})
	}
}

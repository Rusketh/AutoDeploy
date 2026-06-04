package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// RegisterNotifications mounts notification-related API routes.
func RegisterNotifications(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/notifications", requireAuth(r, handleListNotifications(r)))
	mux.HandleFunc("GET /api/v1/notifications/unread-count", requireAuth(r, handleUnreadCount(r)))
	mux.HandleFunc("POST /api/v1/notifications/mark-read", requireAuth(r, handleMarkRead(r)))
	mux.HandleFunc("POST /api/v1/notifications/mark-all-read", requireAuth(r, handleMarkAllRead(r)))
	mux.HandleFunc("GET /api/v1/notifications/preferences", requireAuth(r, handleGetPreferences(r)))
	mux.HandleFunc("PUT /api/v1/notifications/preferences", requireAuth(r, handleSetPreferences(r)))
	mux.HandleFunc("GET /api/v1/notifications/events", requireAuth(r, handleListEvents(r)))
}

func handleListNotifications(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(req.URL.Query().Get("offset"))
		s := model.NotificationSearch{
			UserID:   u.ID,
			Severity: req.URL.Query().Get("severity"),
			Event:    req.URL.Query().Get("event"),
			Unread:   req.URL.Query().Get("unread") == "1",
			Limit:    limit,
			Offset:   offset,
		}
		list, total, err := r.Notifications.List(req.Context(), s)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"notifications": list,
			"total":         total,
		})
	}
}

func handleUnreadCount(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		n, err := r.Notifications.UnreadCount(req.Context(), u.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": n})
	}
}

func handleMarkRead(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		var body struct {
			IDs []model.ID `json:"ids"`
		}
		if err := decodeJSON(req, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		if err := r.Notifications.MarkRead(req.Context(), u.ID, body.IDs); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleMarkAllRead(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		if err := r.Notifications.MarkAllRead(req.Context(), u.ID); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleGetPreferences(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		prefs, err := r.Notifications.GetPreferences(req.Context(), u.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"preferences": prefs,
			"categories":  notify.EventCategories,
		})
	}
}

func handleSetPreferences(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := UserFromRequest(req, r)
		var body []struct {
			Event  string `json:"event"`
			Portal bool   `json:"portal"`
			Email  bool   `json:"email"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		for _, p := range body {
			event := strings.TrimSpace(p.Event)
			if event == "" {
				continue
			}
			if err := r.Notifications.SetPreference(req.Context(), u.ID, event, p.Portal, p.Email); err != nil {
				writeError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleListEvents(_ Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"categories": notify.EventCategories,
			"events":     notify.AllEventNames(),
		})
	}
}

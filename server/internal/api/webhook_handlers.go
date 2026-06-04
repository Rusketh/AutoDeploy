package api

import (
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterWebhooks mounts webhook-related API routes.
func RegisterWebhooks(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/webhooks", requireAuth(r, handleListWebhooks(r)))
	mux.HandleFunc("POST /api/v1/webhooks", requireAuth(r, handleCreateWebhook(r)))
	mux.HandleFunc("GET /api/v1/webhooks/{id}", requireAuth(r, handleGetWebhook(r)))
	mux.HandleFunc("PUT /api/v1/webhooks/{id}", requireAuth(r, handleUpdateWebhook(r)))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", requireAuth(r, handleDeleteWebhook(r)))
	mux.HandleFunc("POST /api/v1/webhooks/{id}/test", requireAuth(r, handleTestWebhook(r)))
	mux.HandleFunc("GET /api/v1/webhooks/{id}/deliveries", requireAuth(r, handleListDeliveries(r)))
}

func handleListWebhooks(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		list, err := r.WebhookRepo.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		safe := make([]map[string]any, len(list))
		for i, wh := range list {
			safe[i] = webhookSafe(wh)
		}
		writeJSON(w, http.StatusOK, safe)
	}
}

func handleCreateWebhook(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var wh model.Webhook
		if err := decodeJSON(req, &wh); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		id, err := r.WebhookRepo.Create(req.Context(), wh)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	}
}

func handleGetWebhook(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		wh, err := r.WebhookRepo.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, webhookSafe(wh))
	}
}

func handleUpdateWebhook(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		var wh model.Webhook
		if err := decodeJSON(req, &wh); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		wh.ID = id
		if err := r.WebhookRepo.Update(req.Context(), wh); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleDeleteWebhook(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		if err := r.WebhookRepo.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleTestWebhook(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		wh, err := r.WebhookRepo.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Emitter != nil && r.Emitter.WebhookSender != nil {
			if err := r.Emitter.WebhookSender.DeliverTest(req.Context(), wh); err != nil {
				writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleListDeliveries(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		list, err := r.WebhookRepo.ListDeliveries(req.Context(), id, limit)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func webhookSafe(wh model.Webhook) map[string]any {
	hasSecret := wh.Secret != ""
	return map[string]any{
		"id":           wh.ID,
		"name":         wh.Name,
		"url":          wh.URL,
		"has_secret":   hasSecret,
		"events":       wh.Events,
		"min_severity": wh.MinSeverity,
		"enabled":      wh.Enabled,
		"created_at":   wh.CreatedAt,
		"updated_at":   wh.UpdatedAt,
	}
}

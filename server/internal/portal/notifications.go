package portal

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/runtime"
)

func registerNotificationRoutes(get, post func(string, http.HandlerFunc), r Repos) {
	// Notification center
	get("/portal/notifications", notificationsPage(r))
	post("/portal/notifications/mark-all-read", notificationsMarkAllRead(r))

	// Notification settings
	get("/portal/settings/notifications", notifySettingsPage(r))
	post("/portal/settings/notifications", notifySettingsSubmit(r))
	post("/portal/settings/notifications/email-test", notifyEmailTest(r))

	// User notification preferences
	get("/portal/settings/notifications/preferences", notifyPreferencesPage(r))
	post("/portal/settings/notifications/preferences", notifyPreferencesSubmit(r))

	// Webhook management
	get("/portal/settings/notifications/webhooks", webhookListPage(r))
	get("/portal/settings/notifications/webhooks/new", webhookFormPage(r, false))
	post("/portal/settings/notifications/webhooks/new", webhookCreateSubmit(r))
	get("/portal/settings/notifications/webhooks/{id}", webhookFormPage(r, true))
	post("/portal/settings/notifications/webhooks/{id}", webhookUpdateSubmit(r))
	post("/portal/settings/notifications/webhooks/{id}/delete", webhookDeleteSubmit(r))
	post("/portal/settings/notifications/webhooks/{id}/test", webhookTestSubmit(r))
	get("/portal/settings/notifications/webhooks/{id}/deliveries", webhookDeliveriesPage(r))
}

func notificationsPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		// Same pager as the other lists (?page=/?size= plus the shared
		// template) instead of the old bespoke ?offset= links. The repo
		// pages in SQL, so ask for the requested window first and let the
		// returned total clamp an out-of-range page (e.g. after deletes).
		pageN, size := pageAndSize(req, 50)
		s := model.NotificationSearch{
			UserID:   u.ID,
			Severity: req.URL.Query().Get("severity"),
			Event:    req.URL.Query().Get("event"),
			Unread:   req.URL.Query().Get("unread") == "1",
			Limit:    size,
			Offset:   (pageN - 1) * size,
		}
		list, total, _ := r.Notifications.List(req.Context(), s)
		page := paginate(req, total, 50)
		if page.Offset != s.Offset {
			s.Offset = page.Offset
			list, _, _ = r.Notifications.List(req.Context(), s)
		}
		render(w, req, r, "notifications.html", "Notifications", map[string]any{
			"Notifications": list,
			"Page":          page,
			"Severity":      s.Severity,
			"Event":         s.Event,
			"Unread":        s.Unread,
			"Categories":    notify.EventCategories,
		})
	}
}

func notificationsMarkAllRead(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		if err := r.Notifications.MarkAllRead(req.Context(), u.ID); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "All notifications marked as read.")
		}
		http.Redirect(w, req, "/portal/notifications", http.StatusFound)
	}
}

func notifySettingsPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var emailCfg runtime.EmailConfig
		var portalEnabled, emailEnabled bool
		var portalRetention, offlineMinutes int
		if r.Runtime != nil {
			portalEnabled = r.Runtime.NotifyPortalEnabled()
			portalRetention = r.Runtime.NotifyPortalRetentionDays()
			emailEnabled = r.Runtime.NotifyEmailEnabled()
			emailCfg = r.Runtime.NotifyEmailConfig(req.Context())
			offlineMinutes = r.Runtime.NotifyMachineOfflineMinutes()
		}
		webhooks, _ := r.WebhookRepo.List(req.Context())
		render(w, req, r, "settings_notifications.html", "Notifications", map[string]any{
			"PortalEnabled":   portalEnabled,
			"PortalRetention": portalRetention,
			"EmailEnabled":    emailEnabled,
			"EmailCfg":        emailCfg,
			"OfflineMinutes":  offlineMinutes,
			"Webhooks":        webhooks,
			"WebhookCount":    len(webhooks),
		})
	}
}

func notifySettingsSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		portalEnabled := req.FormValue("portal_enabled") == "1"
		if err := r.Runtime.SetNotifyPortalEnabled(req.Context(), portalEnabled); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
			return
		}

		if v := req.FormValue("portal_retention"); v != "" {
			n, _ := strconv.Atoi(v)
			_ = r.Runtime.SetNotifyPortalRetentionDays(req.Context(), n)
		}

		emailEnabled := req.FormValue("email_enabled") == "1"
		if err := r.Runtime.SetNotifyEmailEnabled(req.Context(), emailEnabled); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
			return
		}

		cfg := runtime.EmailConfig{
			Host:        strings.TrimSpace(req.FormValue("smtp_host")),
			User:        strings.TrimSpace(req.FormValue("smtp_user")),
			Password:    req.FormValue("smtp_password"),
			TLSMode:     req.FormValue("smtp_tls"),
			FromAddress: strings.TrimSpace(req.FormValue("from_address")),
			FromName:    strings.TrimSpace(req.FormValue("from_name")),
		}
		if v := req.FormValue("smtp_port"); v != "" {
			cfg.Port, _ = strconv.Atoi(v)
		} else {
			cfg.Port = 587
		}
		if err := r.Runtime.SetNotifyEmailConfig(req.Context(), cfg); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
			return
		}

		if v := req.FormValue("offline_minutes"); v != "" {
			n, _ := strconv.Atoi(v)
			if n >= 1 {
				_ = r.Runtime.SetNotifyMachineOfflineMinutes(req.Context(), n)
			}
		}

		flash(w, "ok", "Notification settings saved.")
		http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
	}
}

func notifyEmailTest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		if r.Emitter == nil || r.Emitter.Email == nil {
			flash(w, "err", "Email not configured.")
			http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
			return
		}
		to := u.Email
		if to == "" {
			to = req.FormValue("test_email")
		}
		if to == "" {
			flash(w, "err", "No email address. Set one on your account or enter a test address.")
			http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
			return
		}
		if err := r.Emitter.Email.SendTest(to); err != nil {
			flash(w, "err", "Test email failed: "+err.Error())
		} else {
			flash(w, "ok", "Test email sent to "+to+".")
		}
		http.Redirect(w, req, "/portal/settings/notifications", http.StatusFound)
	}
}

func notifyPreferencesPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		prefs, _ := r.Notifications.GetPreferences(req.Context(), u.ID)
		prefMap := make(map[string]model.NotificationPreference)
		for _, p := range prefs {
			prefMap[p.Event] = p
		}
		render(w, req, r, "settings_notify_preferences.html", "Notification Preferences", map[string]any{
			"Categories": notify.EventCategories,
			"Prefs":      prefMap,
			"User":       u,
		})
	}
}

func notifyPreferencesSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Save email address
		email := strings.TrimSpace(req.FormValue("email"))
		_ = r.Users.SetEmail(req.Context(), u.ID, email)

		for _, ev := range notify.AllEventNames() {
			portal := req.FormValue("portal_"+ev) == "1"
			emailPref := req.FormValue("email_"+ev) == "1"
			_ = r.Notifications.SetPreference(req.Context(), u.ID, ev, portal, emailPref)
		}
		flash(w, "ok", "Notification preferences saved.")
		http.Redirect(w, req, "/portal/settings/notifications/preferences", http.StatusFound)
	}
}

func webhookListPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		webhooks, _ := r.WebhookRepo.List(req.Context())
		render(w, req, r, "webhook_list.html", "Webhooks", map[string]any{
			"Webhooks": webhooks,
		})
	}
}

func webhookFormPage(r Repos, edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var wh model.Webhook
		if edit {
			id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
			wh, _ = r.WebhookRepo.Get(req.Context(), model.ID(id))
		} else {
			wh.Enabled = true
			wh.Events = []string{"*"}
			wh.MinSeverity = "info"
		}
		render(w, req, r, "webhook_form.html", "Webhook", map[string]any{
			"Webhook":    wh,
			"Edit":       edit,
			"Categories": notify.EventCategories,
			"AllEvents":  notify.AllEventNames(),
		})
	}
}

func webhookCreateSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wh := webhookFromForm(req)
		id, err := r.WebhookRepo.Create(req.Context(), wh)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/settings/notifications/webhooks/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Webhook created.")
		http.Redirect(w, req, "/portal/settings/notifications/webhooks/"+strconv.FormatInt(int64(id), 10), http.StatusFound)
	}
}

func webhookUpdateSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wh := webhookFromForm(req)
		wh.ID = model.ID(id)
		if err := r.WebhookRepo.Update(req.Context(), wh); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Webhook saved.")
		}
		http.Redirect(w, req, "/portal/settings/notifications/webhooks/"+req.PathValue("id"), http.StatusFound)
	}
}

func webhookDeleteSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err := r.WebhookRepo.Delete(req.Context(), model.ID(id)); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Webhook deleted.")
		}
		http.Redirect(w, req, "/portal/settings/notifications/webhooks", http.StatusFound)
	}
}

func webhookTestSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		wh, err := r.WebhookRepo.Get(req.Context(), model.ID(id))
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/settings/notifications/webhooks", http.StatusFound)
			return
		}
		if r.Emitter != nil && r.Emitter.WebhookSender != nil {
			_ = r.Emitter.WebhookSender.DeliverTest(req.Context(), wh)
			flash(w, "ok", "Test payload sent. Check deliveries.")
		} else {
			flash(w, "err", "Webhook sender not available.")
		}
		http.Redirect(w, req, "/portal/settings/notifications/webhooks/"+req.PathValue("id")+"/deliveries", http.StatusFound)
	}
}

func webhookDeliveriesPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		wh, _ := r.WebhookRepo.Get(req.Context(), model.ID(id))
		deliveries, _ := r.WebhookRepo.ListDeliveries(req.Context(), model.ID(id), 50)
		render(w, req, r, "webhook_deliveries.html", "Webhook Deliveries", map[string]any{
			"Webhook":    wh,
			"Deliveries": deliveries,
		})
	}
}

func webhookFromForm(req *http.Request) model.Webhook {
	events := req.Form["events"]
	if len(events) == 0 {
		events = []string{"*"}
	}
	return model.Webhook{
		Name:        strings.TrimSpace(req.FormValue("name")),
		URL:         strings.TrimSpace(req.FormValue("url")),
		Secret:      req.FormValue("secret"),
		Events:      events,
		MinSeverity: req.FormValue("min_severity"),
		Enabled:     req.FormValue("enabled") == "1",
	}
}

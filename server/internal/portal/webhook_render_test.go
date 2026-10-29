package portal

import (
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// Editing a webhook must show its saved events ticked: the form posts only
// the ticked boxes, so an unticked list silently dropped every event on save.
func TestWebhookFormChecksSavedEvents(t *testing.T) {
	out := renderBulk(t, "webhook_form.html", map[string]any{
		"Webhook":    model.Webhook{ID: 3, Name: "Teams", Events: []string{notify.EventDeployFailed, notify.EventBulkCompleted}},
		"Edit":       true,
		"Categories": notify.EventCategories,
	})
	for _, ev := range []string{notify.EventDeployFailed, notify.EventBulkCompleted} {
		if !strings.Contains(out, `value="`+ev+`" checked`) {
			t.Errorf("saved event %s not checked", ev)
		}
	}
	if strings.Contains(out, `value="`+notify.EventDeployStarted+`" checked`) {
		t.Errorf("unsaved event %s checked", notify.EventDeployStarted)
	}
	if !strings.Contains(out, "Deployment failed") {
		t.Errorf("event shown without its plain-English label")
	}
}

func TestNotifyPreferencesShowEventLabels(t *testing.T) {
	out := renderBulk(t, "settings_notify_preferences.html", map[string]any{
		"Categories": notify.EventCategories,
		"Prefs":      map[string]model.NotificationPreference{},
		"Email":      "",
	})
	if !strings.Contains(out, "Machine went offline") || strings.Contains(out, "<code>machine.offline</code>") {
		t.Errorf("preferences should name events in plain English")
	}
}
